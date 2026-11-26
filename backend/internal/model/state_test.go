package model

import (
	"testing"
	"time"
)

func TestAlertStateTransitions(t *testing.T) {
	cases := []struct {
		state        AlertState
		ack          bool
		unack        bool
		dismiss      bool
		reopen       bool
		attachNormal bool
		attachRetro  bool
	}{
		{AlertStateFiring, true, false, true, false, true, true},
		{AlertStateAcknowledged, false, true, true, false, true, true},
		{AlertStateDismissed, false, false, false, true, false, false},
		{AlertStateResolved, false, false, false, false, false, true},
	}

	for _, tc := range cases {
		if got := CanAcknowledge(tc.state); got != tc.ack {
			t.Errorf("CanAcknowledge(%s) = %v, want %v", tc.state, got, tc.ack)
		}
		if got := CanUnacknowledge(tc.state); got != tc.unack {
			t.Errorf("CanUnacknowledge(%s) = %v, want %v", tc.state, got, tc.unack)
		}
		if got := CanDismiss(tc.state); got != tc.dismiss {
			t.Errorf("CanDismiss(%s) = %v, want %v", tc.state, got, tc.dismiss)
		}
		if got := CanReopen(tc.state); got != tc.reopen {
			t.Errorf("CanReopen(%s) = %v, want %v", tc.state, got, tc.reopen)
		}
		if got := CanAttachIncident(tc.state, false); got != tc.attachNormal {
			t.Errorf("CanAttachIncident(%s, false) = %v, want %v", tc.state, got, tc.attachNormal)
		}
		if got := CanAttachIncident(tc.state, true); got != tc.attachRetro {
			t.Errorf("CanAttachIncident(%s, true) = %v, want %v", tc.state, got, tc.attachRetro)
		}
	}
}

func TestComputeDriftStatus(t *testing.T) {
	s1 := int16(1)
	s2 := int16(2)
	s3 := int16(3)
	s6 := int16(6)
	s7 := int16(7)
	s8 := int16(8)

	tests := []struct {
		name       string
		alertState AlertState
		inc        *ServiceNowIncident
		want       IncidentDriftStatus
	}{
		{"nil incident", AlertStateResolved, nil, DriftNone},
		{"pending incident ignored", AlertStateResolved, &ServiceNowIncident{SyncStatus: SnowSyncPending, SnowState: &s1}, DriftNone},
		{"failed incident ignored", AlertStateResolved, &ServiceNowIncident{SyncStatus: SnowSyncFailed, SnowState: &s2}, DriftNone},
		{"firing + open New", AlertStateFiring, &ServiceNowIncident{SyncStatus: SnowSyncCreated, SnowState: &s1}, DriftNone},
		{"acknowledged + In Progress", AlertStateAcknowledged, &ServiceNowIncident{SyncStatus: SnowSyncCreated, SnowState: &s2}, DriftNone},
		{"firing + Resolved -> active_closed_ticket", AlertStateFiring, &ServiceNowIncident{SyncStatus: SnowSyncCreated, SnowState: &s6}, DriftActiveClosedTicket},
		{"acknowledged + Closed -> active_closed_ticket", AlertStateAcknowledged, &ServiceNowIncident{SyncStatus: SnowSyncLinked, SnowState: &s7}, DriftActiveClosedTicket},
		{"acknowledged + Canceled -> active_closed_ticket", AlertStateAcknowledged, &ServiceNowIncident{SyncStatus: SnowSyncCreated, SnowState: &s8}, DriftActiveClosedTicket},
		{"resolved + New -> resolved_open_ticket", AlertStateResolved, &ServiceNowIncident{SyncStatus: SnowSyncCreated, SnowState: &s1}, DriftResolvedOpenTicket},
		{"resolved + In Progress -> resolved_open_ticket", AlertStateResolved, &ServiceNowIncident{SyncStatus: SnowSyncLinked, SnowState: &s2}, DriftResolvedOpenTicket},
		{"resolved + On Hold -> resolved_open_ticket", AlertStateResolved, &ServiceNowIncident{SyncStatus: SnowSyncCreated, SnowState: &s3}, DriftResolvedOpenTicket},
		{"resolved + Resolved -> none", AlertStateResolved, &ServiceNowIncident{SyncStatus: SnowSyncCreated, SnowState: &s6}, DriftNone},
		{"dismissed + open ticket -> none", AlertStateDismissed, &ServiceNowIncident{SyncStatus: SnowSyncCreated, SnowState: &s2}, DriftNone},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ComputeDriftStatus(tc.alertState, tc.inc); got != tc.want {
				t.Fatalf("ComputeDriftStatus = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestUUIDAndCursorRoundTrip(t *testing.T) {
	id, err := NewUUID()
	if err != nil {
		t.Fatalf("NewUUID error: %v", err)
	}
	if !IsValidUUID(id) {
		t.Fatalf("generated UUID %q failed IsValidUUID", id)
	}
	if IsValidUUID("not-a-uuid") || IsValidUUID("") {
		t.Fatalf("IsValidUUID accepted invalid value")
	}

	now := time.Now().UTC().Truncate(time.Millisecond)
	rank := 1
	encoded, err := EncodeCursor(PageCursor{
		Sort:         SortSeverityDesc,
		StartsAt:     &now,
		SeverityRank: &rank,
		ID:           id,
	})
	if err != nil {
		t.Fatalf("EncodeCursor error: %v", err)
	}
	decoded, err := DecodeCursor(encoded)
	if err != nil {
		t.Fatalf("DecodeCursor error: %v", err)
	}
	if decoded.ID != id || decoded.Sort != SortSeverityDesc || decoded.SeverityRank == nil || *decoded.SeverityRank != 1 {
		t.Fatalf("unexpected decoded cursor: %+v", decoded)
	}

	inst := "web-01.prod:9100"
	alertName := "HighCPU"
	encodedInst, err := EncodeCursor(PageCursor{
		Sort:         SortInstanceAsc,
		Instance:     &inst,
		InstanceNull: false,
		AlertName:    &alertName,
		ID:           id,
	})
	if err != nil {
		t.Fatalf("EncodeCursor instance error: %v", err)
	}
	decodedInst, err := DecodeCursor(encodedInst)
	if err != nil {
		t.Fatalf("DecodeCursor instance error: %v", err)
	}
	if decodedInst.Sort != SortInstanceAsc || decodedInst.Instance == nil || *decodedInst.Instance != inst || decodedInst.InstanceNull || decodedInst.AlertName == nil || *decodedInst.AlertName != alertName {
		t.Fatalf("unexpected decoded instance cursor: %+v", decodedInst)
	}
}

func TestExtractInstance(t *testing.T) {
	cases := []struct {
		name   string
		labels map[string]string
		want   string
	}{
		{"nil labels", nil, ""},
		{"prefers instance", map[string]string{"instance": " db-01:9100 ", "host": "h1"}, "db-01:9100"},
		{"falls back to host", map[string]string{"instance": "   ", "host": "web-02", "node": "n1"}, "web-02"},
		{"falls back to hostname", map[string]string{"hostname": "app-03", "pod": "p1"}, "app-03"},
		{"falls back to node", map[string]string{"node": "worker-1", "pod": "p1"}, "worker-1"},
		{"falls back to pod", map[string]string{"pod": "api-7f8b9"}, "api-7f8b9"},
		{"empty when absent", map[string]string{"alertname": "HighCPU"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExtractInstance(tc.labels); got != tc.want {
				t.Fatalf("ExtractInstance() = %q, want %q", got, tc.want)
			}
		})
	}
}
