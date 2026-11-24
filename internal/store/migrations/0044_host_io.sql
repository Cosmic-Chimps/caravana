-- Network and disk I/O per server (feature 055). The node agent reports cumulative counters from /proc and
-- the console turns two of them into rates; the sampler writes each server's newest rates into its host row
-- once a minute. NULL is a gap (no agent, an older agent, a counter that reset), never 0.
ALTER TABLE metrics_host ADD COLUMN net_rx_bps INTEGER;      -- bytes per second in on the default-route interface
ALTER TABLE metrics_host ADD COLUMN net_tx_bps INTEGER;      -- bytes per second out on it
ALTER TABLE metrics_host ADD COLUMN disk_read_bps INTEGER;   -- bytes per second read, over the watched devices
ALTER TABLE metrics_host ADD COLUMN disk_write_bps INTEGER;  -- bytes per second written
ALTER TABLE metrics_host ADD COLUMN disk_busy_pct INTEGER;   -- 0-100: the busiest watched device's share of the time it was busy
