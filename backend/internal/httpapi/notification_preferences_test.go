package httpapi

import (
	"encoding/json"
	"testing"
	"time"
)

func TestNestedNotificationPreferencesControlIndependentChannels(t *testing.T) {
	patch, err := decodePreferencesPatch(json.RawMessage(`{
		"notifications":{
			"inApp":{"mentions":false,"signals":false,"echoes":false,"approvals":false},
			"toast":{"enabled":false},"desktop":{"enabled":true},
			"digest":{"mode":"daily","time":"08:30"},
			"quietHours":{"enabled":true,"start":"22:00","end":"07:00"}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	updated, err := applyPreferencesPatch(defaultPreferencesDocument(), patch)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Notifications.InApp.Mentions || updated.Notifications.InApp.Signals || updated.Notifications.InApp.Echoes {
		t.Fatalf("in-app category patch was not applied: %+v", updated.Notifications.InApp)
	}
	if !updated.Notifications.InApp.Approvals {
		t.Fatal("mandatory approval notifications were disabled")
	}
	if updated.Notifications.Toast.Enabled || !updated.Notifications.Desktop.Enabled || updated.Notifications.Digest.Mode != "daily" {
		t.Fatalf("channel patch was not applied: %+v", updated.Notifications)
	}
}

func TestLegacyDesktopPlaceholderDoesNotGrantNewChannelConsent(t *testing.T) {
	patch, err := decodePreferencesPatch(json.RawMessage(`{"notifications":{"desktop":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	updated, err := applyPreferencesPatch(defaultPreferencesDocument(), patch)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Notifications.Desktop.Enabled {
		t.Fatal("legacy placeholder enabled the new desktop channel without consent")
	}
}

func TestNotificationCategoryPolicyKeepsOperationalEvents(t *testing.T) {
	preferences := defaultPreferencesDocument().Notifications
	preferences.InApp.Mentions = false
	preferences.InApp.Signals = false
	preferences.InApp.Follows = false
	preferences.InApp.Echoes = false
	for _, kind := range []string{"mention", "reaction", "follow", "reply", "quote", "remoin"} {
		if notificationInAppEnabled(preferences, kind) {
			t.Errorf("disabled category %q was stored", kind)
		}
	}
	for _, kind := range []string{"approval_requested", "approval_approved", "approval_rejected", "security", "future_operational_event"} {
		if !notificationInAppEnabled(preferences, kind) {
			t.Errorf("operational category %q was dropped", kind)
		}
	}
}

func TestQuietHoursWrapMidnight(t *testing.T) {
	quiet := notificationQuietHoursPreferences{Enabled: true, Start: "22:00", End: "07:00"}
	for _, hour := range []int{22, 23, 0, 6} {
		if !notificationQuietAt(quiet, time.Date(2026, 8, 30, hour, 30, 0, 0, time.UTC)) {
			t.Errorf("hour %d should be quiet", hour)
		}
	}
	for _, hour := range []int{7, 12, 21} {
		if notificationQuietAt(quiet, time.Date(2026, 8, 30, hour, 30, 0, 0, time.UTC)) {
			t.Errorf("hour %d should not be quiet", hour)
		}
	}
}

func TestClockTimeRejectsNonDigitsAndOutOfRangeValues(t *testing.T) {
	for _, value := range []string{"1a:0a", "24:00", "12:60", "8:00", "08:0", "08:00:00"} {
		if validClockTime(value) {
			t.Errorf("invalid clock time %q accepted", value)
		}
	}
	for _, value := range []string{"00:00", "08:30", "23:59"} {
		if !validClockTime(value) {
			t.Errorf("valid clock time %q rejected", value)
		}
	}
}

func TestDigestDueUsesServiceTimezoneAndMode(t *testing.T) {
	seoul, err := time.LoadLocation("Asia/Seoul")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 30, 0, 5, 0, 0, time.UTC) // 09:05 KST
	last := now.Add(-24 * time.Hour)
	if due, window := digestDue(notificationDigestPreferences{Mode: "daily", Time: "09:00"}, last, now, seoul); !due || window != "2026-08-30" {
		t.Fatalf("daily due=%t window=%q", due, window)
	}
	if due, _ := digestDue(notificationDigestPreferences{Mode: "daily", Time: "10:00"}, last, now, seoul); due {
		t.Fatal("daily digest ran before configured local time")
	}
	if due, _ := digestDue(notificationDigestPreferences{Mode: "hourly", Time: "08:00"}, now.Add(-59*time.Minute), now, seoul); due {
		t.Fatal("hourly digest ran too early")
	}
	if due, _ := digestDue(notificationDigestPreferences{Mode: "hourly", Time: "08:00"}, now.Add(-61*time.Minute), now, seoul); !due {
		t.Fatal("hourly digest did not run after one hour")
	}
}

func TestLooseDigestSignatureMatchesLegacyDefaults(t *testing.T) {
	tests := []struct {
		payload json.RawMessage
		want    string
	}{
		{json.RawMessage(`{}`), "off"},
		{json.RawMessage(`{"notifications":{"unknown":true}}`), "off"},
		{json.RawMessage(`{"notifications":{"digest":{"mode":"daily"},"unknown":true}}`), "daily@08:00"},
		{json.RawMessage(`{"notifications":{"digest":{"mode":"hourly"},"unknown":true}}`), "hourly"},
	}
	for _, test := range tests {
		if got := looseDigestConfigSignature(test.payload); got != test.want {
			t.Errorf("looseDigestConfigSignature(%s) = %q, want %q", test.payload, got, test.want)
		}
	}
}
