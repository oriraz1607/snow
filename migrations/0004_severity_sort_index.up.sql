-- Support fast Critical-before-Warning keyset pagination on active alert queues.

CREATE INDEX idx_alerts_active_severity_rank
    ON alerts (
        state,
        (CASE severity WHEN 'critical' THEN 1 WHEN 'warning' THEN 2 WHEN 'info' THEN 3 ELSE 4 END),
        starts_at DESC,
        id DESC
    )
    WHERE state IN ('firing', 'acknowledged', 'dismissed')
      AND severity IN ('critical', 'warning');
