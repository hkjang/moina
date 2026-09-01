package httpapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/hkjang/moina/backend/internal/observability"
	"github.com/hkjang/moina/backend/internal/outbound"
	"golang.org/x/oauth2"
)

type oidcExchangeFailure struct {
	Status         int
	Code           string
	Message        string
	Reason         string
	OAuthErrorCode string
	UpstreamStatus int
}

type oidcProviderMetadata struct {
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
}

func oidcTokenAuthMethods(provider *oidc.Provider) []string {
	var metadata oidcProviderMetadata
	if provider == nil || provider.Claims(&metadata) != nil {
		return nil
	}
	return metadata.TokenEndpointAuthMethodsSupported
}

// oidcTokenAuthStyle avoids oauth2's two-request auth-style probe whenever
// discovery gives us enough information. In particular, a public Keycloak
// client must send client_id in the form body and must not send Basic auth.
func oidcTokenAuthStyle(clientSecret string, supported []string) oauth2.AuthStyle {
	if clientSecret == "" {
		return oauth2.AuthStyleInParams
	}
	for _, method := range supported {
		if strings.EqualFold(strings.TrimSpace(method), "client_secret_basic") {
			return oauth2.AuthStyleInHeader
		}
	}
	for _, method := range supported {
		if strings.EqualFold(strings.TrimSpace(method), "client_secret_post") {
			return oauth2.AuthStyleInParams
		}
	}
	return oauth2.AuthStyleAutoDetect
}

func oidcTokenAuthMethod(clientSecret string, style oauth2.AuthStyle) string {
	if clientSecret == "" {
		return "none"
	}
	if style == oauth2.AuthStyleInHeader {
		return "client_secret_basic"
	}
	if style == oauth2.AuthStyleInParams {
		return "client_secret_post"
	}
	return "auto"
}

// probeOIDCTokenExchange submits an intentionally unusable authorization code.
// invalid_grant proves that the provider accepted the client authentication and
// parsed the Authorization Code + PKCE request, without creating a session or
// consuming a real authorization code.
func probeOIDCTokenExchange(ctx context.Context, config *oauth2.Config) error {
	_, err := config.Exchange(ctx, oauth2.GenerateVerifier(), oauth2.VerifierOption(oauth2.GenerateVerifier()))
	if err == nil {
		return errors.New("OIDC token endpoint accepted an invalid authorization code")
	}
	var retrieveError *oauth2.RetrieveError
	if errors.As(err, &retrieveError) && strings.EqualFold(strings.TrimSpace(retrieveError.ErrorCode), "invalid_grant") {
		return nil
	}
	return err
}

func classifyOIDCExchangeError(err error) oidcExchangeFailure {
	failure := oidcExchangeFailure{
		Status:  http.StatusBadGateway,
		Code:    "oidc_exchange_failed",
		Message: "Keycloak 토큰 응답을 확인할 수 없습니다. 연결 테스트에서 Client 인증과 토큰 endpoint 상태를 확인해 주세요.",
		Reason:  "invalid_token_response",
	}

	if errors.Is(err, outbound.ErrUnsafeAddress) {
		failure.Code = "oidc_private_host_denied"
		failure.Message = "OIDC 토큰 endpoint 주소가 안전 정책에 의해 차단되었습니다. 사설망 hostname을 전체·사설망 OIDC Host에 모두 등록해 주세요."
		failure.Reason = "unsafe_address"
		return failure
	}
	if errors.Is(err, outbound.ErrHostNotAllowed) {
		failure.Code = "oidc_egress_denied"
		failure.Message = "OIDC 토큰 endpoint 또는 redirect hostname이 허용 목록에 없습니다. 정확한 host 또는 host:port를 OIDC 허용 Host에 등록해 주세요."
		failure.Reason = "host_not_allowed"
		return failure
	}
	if isOIDCCertificateError(err) {
		failure.Code = "oidc_token_tls_failed"
		failure.Message = "OIDC 토큰 endpoint의 TLS 인증서를 확인할 수 없습니다. hostname, 만료일, 인증서 체인과 CA bundle을 확인해 주세요."
		failure.Reason = "tls_certificate"
		return failure
	}
	if isOIDCTimeout(err) {
		failure.Code = "oidc_token_timeout"
		failure.Message = "OIDC 토큰 교환 시간이 초과되었습니다. Keycloak 연결, 방화벽과 응답 시간을 확인한 뒤 로그인을 다시 시작해 주세요."
		failure.Reason = "timeout"
		return failure
	}
	var dnsError *net.DNSError
	if errors.As(err, &dnsError) {
		failure.Code = "oidc_token_dns_failed"
		failure.Message = "OIDC 토큰 endpoint DNS를 확인할 수 없습니다. MOINA 컨테이너의 DNS와 Issuer hostname을 확인해 주세요."
		failure.Reason = "dns"
		return failure
	}

	var retrieveError *oauth2.RetrieveError
	if !errors.As(err, &retrieveError) {
		return failure
	}
	failure.OAuthErrorCode = strings.ToLower(strings.TrimSpace(retrieveError.ErrorCode))
	if retrieveError.Response != nil {
		failure.UpstreamStatus = retrieveError.Response.StatusCode
	}
	switch failure.OAuthErrorCode {
	case "invalid_client":
		failure.Status = http.StatusUnauthorized
		failure.Code = "oidc_client_auth_failed"
		failure.Message = "Keycloak이 Client 인증을 거부했습니다. Confidential client라면 Client Secret을 다시 입력하고, Public client라면 ‘저장된 Client Secret 삭제’를 켜서 저장한 뒤 Keycloak의 Client authentication을 끄세요."
		failure.Reason = "client_authentication"
	case "unauthorized_client":
		failure.Status = http.StatusUnauthorized
		failure.Code = "oidc_client_not_allowed"
		failure.Message = "Keycloak Client가 Authorization Code 교환을 허용하지 않습니다. Client의 Standard flow를 활성화하고 서비스 계정 전용 Client가 아닌지 확인해 주세요."
		failure.Reason = "client_authorization"
	case "invalid_grant":
		failure.Status = http.StatusUnauthorized
		failure.Code = "oidc_code_rejected"
		failure.Message = "Keycloak이 인증 코드를 거부했습니다. SSO 로그인을 처음부터 다시 시도하세요. 반복되면 Standard flow, PKCE S256, 정확한 Redirect URI와 Keycloak·MOINA 서버 시간 동기화를 확인해 주세요."
		failure.Reason = "authorization_code"
	case "invalid_request", "unsupported_grant_type":
		failure.Status = http.StatusBadRequest
		failure.Code = "oidc_token_request_rejected"
		failure.Message = "Keycloak 토큰 요청이 Client 설정과 맞지 않습니다. Standard flow와 PKCE Code Challenge Method S256을 활성화해 주세요."
		failure.Reason = "token_request"
	case "temporarily_unavailable", "server_error":
		failure.Code = "oidc_token_unavailable"
		failure.Message = "Keycloak 토큰 endpoint가 일시적으로 요청을 처리할 수 없습니다. 상태를 확인한 뒤 로그인을 다시 시작해 주세요."
		failure.Reason = "provider_unavailable"
	}
	return failure
}

func writeOIDCExchangeError(w http.ResponseWriter, r *http.Request, err error) {
	failure := classifyOIDCExchangeError(err)
	observability.Logger(r.Context()).WarnContext(
		r.Context(),
		"OIDC token 교환 실패",
		"error_code", failure.Code,
		"failure_reason", failure.Reason,
		"oauth_error_code", failure.OAuthErrorCode,
		"upstream_status", failure.UpstreamStatus,
		"cause_type", deepestOIDCDiscoveryErrorType(err),
	)
	writeError(w, failure.Status, failure.Code, failure.Message)
}

func writeAdminOIDCExchangeError(w http.ResponseWriter, r *http.Request, err error) {
	if writeAdminOIDCPolicyError(w, r, err, "token_endpoint") {
		return
	}
	writeOIDCExchangeError(w, r, err)
}
