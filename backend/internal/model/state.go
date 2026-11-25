package model

import "strings"

// SnowStateLabel maps standard out-of-the-box ServiceNow incident state integers to labels.
func SnowStateLabel(state int16) string {
	switch state {
	case 1:
		return "New"
	case 2:
		return "In Progress"
	case 3:
		return "On Hold"
	case 6:
		return "Resolved"
	case 7:
		return "Closed"
	case 8:
		return "Canceled"
	default:
		return "Unknown"
	}
}

// IsValidSnowState reports whether state is one of the allowed ServiceNow incident states.
func IsValidSnowState(state int16) bool {
	switch state {
	case 1, 2, 3, 6, 7, 8:
		return true
	default:
		return false
	}
}

// IsSnowStateOpen reports whether a ServiceNow incident state is still active/open (1, 2, 3).
func IsSnowStateOpen(state *int16) bool {
	if state == nil {
		return false
	}
	return *state == 1 || *state == 2 || *state == 3
}

// IsSnowStateClosed reports whether a ServiceNow incident state is terminal/closed (6, 7, 8).
func IsSnowStateClosed(state *int16) bool {
	if state == nil {
		return false
	}
	return *state == 6 || *state == 7 || *state == 8
}

// ComputeDriftStatus computes cross-system alert-to-incident drift per LLD §4.2.
func ComputeDriftStatus(alertState AlertState, inc *ServiceNowIncident) IncidentDriftStatus {
	if inc == nil {
		return DriftNone
	}
	if inc.SyncStatus != SnowSyncCreated && inc.SyncStatus != SnowSyncLinked {
		return DriftNone
	}
	if alertState == AlertStateResolved && IsSnowStateOpen(inc.SnowState) {
		return DriftResolvedOpenTicket
	}
	if (alertState == AlertStateFiring || alertState == AlertStateAcknowledged) && IsSnowStateClosed(inc.SnowState) {
		return DriftActiveClosedTicket
	}
	return DriftNone
}

// CanAcknowledge reports whether an operator can transition from state to acknowledged.
func CanAcknowledge(state AlertState) bool {
	return state == AlertStateFiring
}

// CanUnacknowledge reports whether an operator can transition from state to firing.
func CanUnacknowledge(state AlertState) bool {
	return state == AlertStateAcknowledged
}

// CanDismiss reports whether an operator can transition from state to dismissed.
func CanDismiss(state AlertState) bool {
	return state == AlertStateFiring || state == AlertStateAcknowledged
}

// CanReopen reports whether an operator can transition from state to firing.
func CanReopen(state AlertState) bool {
	return state == AlertStateDismissed
}

// CanAttachIncident reports whether an alert in state may have a ServiceNow incident created or linked.
func CanAttachIncident(state AlertState, allowRetrospective bool) bool {
	if state == AlertStateAcknowledged {
		return true
	}
	return allowRetrospective && state == AlertStateResolved
}

// SeverityRank maps alert severity strings to an integer ordering (1 = highest).
func SeverityRank(severity string) int {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "critical":
		return 1
	case "warning":
		return 2
	case "info":
		return 3
	default:
		return 4
	}
}

// DefaultImpactUrgency maps alert severity to ServiceNow impact/urgency (1=High, 2=Medium, 3=Low).
func DefaultImpactUrgency(severity string) int16 {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "critical":
		return 1
	case "warning":
		return 2
	default:
		return 3
	}
}
