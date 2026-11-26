/**
 * ServiceNow Business Rule: Prevent Premature Closure While Grafana Alert Is Active
 *
 * Deployment Target (checked-in artifact only — do NOT push automatically):
 *   - Table: incident
 *   - Active: true
 *   - Advanced: true
 *   - When: before
 *   - Update: true (Insert: false)
 *   - Filter Condition:
 *       current.correlation_display == 'Grafana NOC Alert Manager'
 *       && current.state.changes()
 *       && (current.state == 6 || current.state == 7)
 *
 * Required ServiceNow System Properties (sys_properties):
 *   - x_grafana_noc.api_base_url    : Base URL of the Go Alert Manager API (e.g. https://noc-alerts-api.corp.internal)
 *   - x_grafana_noc.webhook_secret  : Shared secret matching SNOW_INBOUND_WEBHOOK_SECRET (stored as password2/encrypted property)
 *   - x_grafana_noc.override_role   : Optional role allowed to bypass this check (default: "x_grafana_noc.force_close"; "admin" always bypasses)
 */
(function executeRule(current, previous /*null when async*/) {
    var CORRELATION_DISPLAY = 'Grafana NOC Alert Manager';
    var HTTP_TIMEOUT_MS = 3000;

    if (current.getValue('correlation_display') !== CORRELATION_DISPLAY) {
        return;
    }

    var targetState = parseInt(current.getValue('state'), 10);
    if (!current.state.changes() || (targetState !== 6 && targetState !== 7)) {
        return;
    }

    // Escape hatch for ServiceNow admins or designated emergency override role.
    var overrideRole = gs.getProperty('x_grafana_noc.override_role', 'x_grafana_noc.force_close');
    if (gs.hasRole('admin') || (overrideRole && gs.hasRole(overrideRole))) {
        return;
    }

    var baseUrl = (gs.getProperty('x_grafana_noc.api_base_url', '') || '').replace(/\/+$/, '');
    var secret = gs.getProperty('x_grafana_noc.webhook_secret', '');
    if (!baseUrl || !secret) {
        gs.warn('[Grafana NOC] Missing x_grafana_noc.api_base_url or x_grafana_noc.webhook_secret; failing open.');
        return;
    }

    var sysId = current.getUniqueValue();
    if (!sysId) {
        return;
    }

    try {
        var rm = new sn_ws.RESTMessageV2();
        rm.setEndpoint(baseUrl + '/api/v1/integrations/servicenow/incidents/' + encodeURIComponent(sysId) + '/active-status');
        rm.setHttpMethod('GET');
        rm.setHttpTimeout(HTTP_TIMEOUT_MS);
        rm.setRequestHeader('Accept', 'application/json');
        rm.setRequestHeader('Authorization', 'Bearer ' + secret);

        var response = rm.execute();
        var httpStatus = response.getStatusCode();

        // Fail-open on 5xx, timeout (status 0), or unexpected status so engineers are never locked out during API outages.
        if (httpStatus !== 200) {
            gs.warn('[Grafana NOC] Active alert check returned HTTP ' + httpStatus + ' for incident ' + current.getValue('number') + '; failing open.');
            return;
        }

        var payload = JSON.parse(response.getBody() || '{}');
        if (payload && payload.has_active_alerts === true) {
            var count = payload.active_alert_count || (payload.active_alerts ? payload.active_alerts.length : 1);
            var alertNames = [];
            if (Array.isArray(payload.active_alerts)) {
                for (var i = 0; i < payload.active_alerts.length && i < 5; i++) {
                    if (payload.active_alerts[i] && payload.active_alerts[i].alert_name) {
                        alertNames.push(payload.active_alerts[i].alert_name);
                    }
                }
            }
            var suffix = alertNames.length > 0 ? ' (' + alertNames.join(', ') + ')' : '';
            gs.addErrorMessage(
                'Cannot resolve or close incident ' + current.getValue('number') + ': ' + count +
                ' linked Grafana/Prometheus alert(s) are still firing' + suffix +
                '. Wait until the alert(s) recover or use an authorized override role.'
            );
            current.setAbortAction(true);
        }
    } catch (ex) {
        gs.warn('[Grafana NOC] Failed checking active alerts for incident ' + current.getValue('number') + ': ' + ex + '; failing open.');
    }
})(current, previous);
