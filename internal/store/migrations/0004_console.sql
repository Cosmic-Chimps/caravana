-- Recent deployments across all apps, for the Apps page's activity and GET /v1/deployments.
CREATE INDEX deployments_by_time ON deployments (created_at DESC);
