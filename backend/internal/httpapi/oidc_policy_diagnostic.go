package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/hkjang/moina/backend/internal/observability"
	"github.com/hkjang/moina/backend/internal/outbound"
)

type oidcPolicyDiagnostic struct {
	Stage             string   `json:"stage"`
	TargetHost        string   `json:"targetHost,omitempty"`
	ResolvedAddresses []string `json:"resolvedAddresses,omitempty"`
	AddressReason     string   `json:"addressReason,omitempty"`
	Action            string   `json:"action"`
	CanAllowPrivate   bool     `json:"canAllowPrivate"`
}

type oidcAdminPolicyFailure struct {
	Code       string
	Message    string
	Reason     string
	Diagnostic oidcPolicyDiagnostic
}

func classifyAdminOIDCPolicyError(err error, stage string) (oidcAdminPolicyFailure, bool) {
	if !errors.Is(err, outbound.ErrHostNotAllowed) && !errors.Is(err, outbound.ErrUnsafeAddress) {
		return oidcAdminPolicyFailure{}, false
	}

	diagnostic := oidcPolicyDiagnostic{Stage: stage, Action: "review_network_policy"}
	var policyError *outbound.PolicyError
	if errors.As(err, &policyError) {
		diagnostic.TargetHost = policyError.Authority
		diagnostic.ResolvedAddresses = slices.Clone(policyError.ResolvedAddresses)
		slices.Sort(diagnostic.ResolvedAddresses)
		diagnostic.AddressReason = policyError.Reason
		diagnostic.CanAllowPrivate = policyError.CanAllowPrivate
	}
	if diagnostic.TargetHost == "" {
		var requestError *url.Error
		if errors.As(err, &requestError) {
			diagnostic.TargetHost = outbound.EndpointAuthority(requestError.URL)
		}
	}
	target := diagnostic.TargetHost
	if target == "" {
		target = "요청 대상"
	}

	if errors.Is(err, outbound.ErrHostNotAllowed) {
		diagnostic.Action = "add_allowed_host"
		return oidcAdminPolicyFailure{
			Code:       "oidc_host_required",
			Message:    "OIDC " + oidcStageLabel(stage) + " 대상 ‘" + target + "’이 OIDC 허용 Host에 없습니다. 이 hostname 또는 host:port를 표시된 그대로 한 줄로 추가하고 다시 저장해 주세요.",
			Reason:     outbound.PolicyReasonHostNotAllowed,
			Diagnostic: diagnostic,
		}, true
	}

	addresses := strings.Join(diagnostic.ResolvedAddresses, ", ")
	if addresses == "" {
		addresses = "확인되지 않음"
	}
	if diagnostic.CanAllowPrivate {
		diagnostic.Action = "add_private_host"
		return oidcAdminPolicyFailure{
			Code:       "oidc_private_host_required",
			Message:    "OIDC " + oidcStageLabel(stage) + " 대상 ‘" + target + "’의 DNS 결과(" + addresses + ")가 사설망 주소입니다. ‘" + target + "’을 OIDC 허용 Host와 사설망 OIDC Host 양쪽에 표시된 그대로 한 줄씩 추가해 주세요.",
			Reason:     outbound.PolicyReasonPrivateNotAllowed,
			Diagnostic: diagnostic,
		}, true
	}

	diagnostic.Action = "change_dns_or_endpoint"
	reasonLabel := oidcAddressReasonLabel(diagnostic.AddressReason)
	return oidcAdminPolicyFailure{
		Code:       "oidc_address_forbidden",
		Message:    "OIDC " + oidcStageLabel(stage) + " 대상 ‘" + target + "’의 DNS 결과(" + addresses + ")가 " + reasonLabel + " 주소입니다. 이 주소는 Host 목록에 등록해도 항상 차단됩니다. Keycloak/DNS가 MOINA 컨테이너에서 도달 가능한 RFC1918 사설 주소 또는 공인 주소를 반환하도록 변경해 주세요.",
		Reason:     diagnostic.AddressReason,
		Diagnostic: diagnostic,
	}, true
}

func writeAdminOIDCPolicyError(w http.ResponseWriter, r *http.Request, err error, stage string) bool {
	failure, ok := classifyAdminOIDCPolicyError(err, stage)
	if !ok {
		return false
	}
	observability.Logger(r.Context()).WarnContext(
		r.Context(),
		"OIDC network 정책 차단",
		"error_code", failure.Code,
		"failure_reason", failure.Reason,
		"policy_stage", stage,
		"target_host", failure.Diagnostic.TargetHost,
		"cause_type", deepestOIDCDiscoveryErrorType(err),
	)
	writeErrorDetails(w, http.StatusBadGateway, failure.Code, failure.Message, failure.Diagnostic)
	return true
}

func oidcStageLabel(stage string) string {
	switch stage {
	case "discovery":
		return "Discovery"
	case "discovery_endpoint":
		return "Discovery 문서 endpoint"
	case "authorization_endpoint":
		return "Authorization endpoint"
	case "token_endpoint":
		return "Token endpoint"
	default:
		return "연결"
	}
}

func oidcAddressReasonLabel(reason string) string {
	switch reason {
	case outbound.PolicyReasonLoopback:
		return "loopback(127.0.0.0/8 또는 ::1)"
	case outbound.PolicyReasonLinkLocal:
		return "link-local"
	case outbound.PolicyReasonMetadata:
		return "cloud metadata"
	case outbound.PolicyReasonCarrierGradeNAT:
		return "CGNAT(100.64.0.0/10)"
	case outbound.PolicyReasonSpecialUse:
		return "예약·특수 용도"
	case outbound.PolicyReasonNonGlobal:
		return "비전역"
	case outbound.PolicyReasonMixedUnsafe:
		return "여러 종류의 차단"
	default:
		return "안전 정책상 허용할 수 없는"
	}
}
