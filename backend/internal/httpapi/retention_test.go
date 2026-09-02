package httpapi

import (
	"strings"
	"testing"
)

func TestDefaultRetentionKeepsTheAuditTrail(t *testing.T) {
	// An upgrade must not silently start deleting the compliance record. Every
	// other table here is pure operational churn and is safe to bound.
	cfg := defaultRetention()
	if cfg.AuditDays != 0 {
		t.Fatalf("auditDays=%d, 기본값은 무기한(0)이어야 합니다", cfg.AuditDays)
	}
	if cfg.NotificationDays <= 0 || cfg.OutboxDays <= 0 || cfg.AIUsageDays <= 0 {
		t.Fatalf("운영 테이블 보존 기본값이 비활성입니다: %+v", cfg)
	}
	if err := validateRetention(cfg); err != nil {
		t.Fatalf("기본 보존 정책이 자체 검증을 통과하지 못합니다: %v", err)
	}
}

func TestValidateRetentionBoundsEveryWindow(t *testing.T) {
	tests := []struct {
		name    string
		cfg     retentionConfig
		wantErr bool
	}{
		{name: "모두 무기한", cfg: retentionConfig{}},
		{name: "상한", cfg: retentionConfig{AuditDays: retentionMaxDays, NotificationDays: retentionMaxDays, OutboxDays: retentionMaxDays, AIUsageDays: retentionMaxDays}},
		{name: "음수 감사", cfg: retentionConfig{AuditDays: -1}, wantErr: true},
		{name: "음수 알림", cfg: retentionConfig{NotificationDays: -1}, wantErr: true},
		{name: "음수 Outbox", cfg: retentionConfig{OutboxDays: -1}, wantErr: true},
		{name: "음수 AI", cfg: retentionConfig{AIUsageDays: -1}, wantErr: true},
		{name: "상한 초과", cfg: retentionConfig{AuditDays: retentionMaxDays + 1}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateRetention(test.cfg); (err != nil) != test.wantErr {
				t.Fatalf("validateRetention(%+v)=%v, wantErr=%v", test.cfg, err, test.wantErr)
			}
		})
	}
}

func TestRetentionSweepsOnlyDeleteDeliveredOutboxEvents(t *testing.T) {
	// A dead letter waits for an administrator to retry it. Ageing it out would
	// silently discard the delivery the operator still has to account for.
	var outbox string
	for _, sweep := range retentionSweeps(retentionConfig{OutboxDays: 14}) {
		if sweep.name == "outbox_events" {
			outbox = sweep.query
		}
	}
	if outbox == "" {
		t.Fatal("outbox_events sweep을 찾을 수 없습니다")
	}
	if !strings.Contains(outbox, "delivered_at IS NOT NULL") {
		t.Fatalf("Outbox sweep이 미전달 이벤트를 보호하지 않습니다: %s", outbox)
	}
	if strings.Contains(outbox, "created_at") {
		t.Fatalf("Outbox sweep은 전달 시각을 기준으로 해야 합니다: %s", outbox)
	}
}

func TestRetentionSweepsAreBatchedAndTargetOneTableEach(t *testing.T) {
	seen := make(map[string]bool)
	for _, sweep := range retentionSweeps(defaultRetention()) {
		if seen[sweep.name] {
			t.Fatalf("%s sweep이 중복됩니다", sweep.name)
		}
		seen[sweep.name] = true
		if !strings.HasPrefix(sweep.query, "DELETE FROM "+sweep.name+" ") {
			t.Fatalf("%s sweep이 다른 테이블을 지웁니다: %s", sweep.name, sweep.query)
		}
		if !strings.Contains(sweep.query, "LIMIT $2") {
			t.Fatalf("%s sweep이 배치 크기를 적용하지 않습니다: %s", sweep.name, sweep.query)
		}
		if !strings.Contains(sweep.query, "<$1") {
			t.Fatalf("%s sweep이 cutoff를 적용하지 않습니다: %s", sweep.name, sweep.query)
		}
	}
	for _, table := range []string{"audit_events", "notifications", "outbox_events", "ai_usage_events"} {
		if !seen[table] {
			t.Fatalf("%s에 보존 sweep이 없습니다", table)
		}
	}
}

func TestRetentionSweepsSkipDisabledWindows(t *testing.T) {
	// purgeExpiredRecords skips a window of zero; assert the configuration that
	// drives that decision carries the zero through rather than defaulting.
	for _, sweep := range retentionSweeps(retentionConfig{NotificationDays: 30}) {
		if sweep.name == "notifications" && sweep.days != 30 {
			t.Fatalf("notifications days=%d, 30을 기대했습니다", sweep.days)
		}
		if sweep.name != "notifications" && sweep.days != 0 {
			t.Fatalf("%s days=%d, 비활성(0)을 기대했습니다", sweep.name, sweep.days)
		}
	}
}
