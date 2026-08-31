package httpapi

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/hkjang/moina/backend/internal/observability"
	"github.com/hkjang/moina/backend/internal/outbound"
)

type oidcDiscoveryFailure struct {
	Status  int
	Code    string
	Message string
	Reason  string
}

func classifyOIDCDiscoveryError(err error) oidcDiscoveryFailure {
	failure := oidcDiscoveryFailure{
		Status:  http.StatusBadGateway,
		Code:    "oidc_unavailable",
		Message: "OIDC discovery 응답을 확인할 수 없습니다. Issuer URL과 /.well-known/openid-configuration 응답을 확인해 주세요.",
		Reason:  "invalid_discovery_response",
	}

	if errors.Is(err, outbound.ErrUnsafeAddress) {
		failure.Code = "oidc_private_host_denied"
		failure.Message = "OIDC 제공자 주소가 안전 정책에 의해 차단되었습니다. 사설망 DNS hostname이면 같은 host를 전체·사설망 OIDC Host에 모두 등록하고, IP·loopback·link-local 주소는 사용하지 마세요."
		failure.Reason = "unsafe_address"
		return failure
	}
	if errors.Is(err, outbound.ErrHostNotAllowed) {
		failure.Code = "oidc_egress_denied"
		failure.Message = "OIDC 제공자 또는 discovery redirect hostname이 허용 목록에 없습니다. 정확한 host 또는 host:port를 OIDC 허용 Host에 등록해 주세요."
		failure.Reason = "host_not_allowed"
		return failure
	}

	var issuerMismatch *oidc.IssuerMismatchError
	if errors.As(err, &issuerMismatch) {
		failure.Code = "oidc_issuer_mismatch"
		failure.Message = "OIDC discovery의 issuer가 입력한 Issuer URL과 일치하지 않습니다. 제공자의 공개 hostname, scheme, path와 trailing slash를 정확히 입력해 주세요."
		failure.Reason = "issuer_mismatch"
		return failure
	}
	if isOIDCCertificateError(err) {
		failure.Code = "oidc_tls_failed"
		failure.Message = "OIDC 제공자의 TLS 인증서를 확인할 수 없습니다. hostname, 만료일, 인증서 체인과 CA bundle을 확인해 주세요."
		failure.Reason = "tls_certificate"
		return failure
	}
	if isOIDCTimeout(err) {
		failure.Code = "oidc_timeout"
		failure.Message = "OIDC discovery 응답 시간이 초과되었습니다. 제공자 연결, 방화벽과 응답 시간을 확인해 주세요."
		failure.Reason = "timeout"
		return failure
	}

	var dnsError *net.DNSError
	if errors.As(err, &dnsError) {
		failure.Code = "oidc_dns_failed"
		failure.Message = "OIDC 제공자 DNS를 확인할 수 없습니다. MOINA 컨테이너의 DNS와 Issuer hostname을 확인해 주세요."
		failure.Reason = "dns"
	}
	return failure
}

func isOIDCCertificateError(err error) bool {
	var verificationError *tls.CertificateVerificationError
	if errors.As(err, &verificationError) {
		return true
	}
	var unknownAuthority x509.UnknownAuthorityError
	var hostnameError x509.HostnameError
	var certificateInvalid x509.CertificateInvalidError
	var systemRoots x509.SystemRootsError
	return errors.As(err, &unknownAuthority) ||
		errors.As(err, &hostnameError) ||
		errors.As(err, &certificateInvalid) ||
		errors.As(err, &systemRoots)
}

func isOIDCTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var networkError net.Error
	return errors.As(err, &networkError) && networkError.Timeout()
}

func writeOIDCDiscoveryError(w http.ResponseWriter, r *http.Request, err error) {
	failure := classifyOIDCDiscoveryError(err)
	observability.Logger(r.Context()).WarnContext(
		r.Context(),
		"OIDC discovery 실패",
		"error_code", failure.Code,
		"failure_reason", failure.Reason,
		"cause_type", deepestOIDCDiscoveryErrorType(err),
	)
	writeError(w, failure.Status, failure.Code, failure.Message)
}

func deepestOIDCDiscoveryErrorType(err error) string {
	if err == nil {
		return "<nil>"
	}
	for {
		next := errors.Unwrap(err)
		if next == nil {
			return fmt.Sprintf("%T", err)
		}
		err = next
	}
}
