-- 237: accounts.disable_auto_temp_unschedulable：永不自动临时不可调度开关。
-- 开启后任何错误路径都不得将该账号写入 temp_unschedulable_until/reason，
-- 由 repository 层在 UPDATE 中原子拦截，无需额外查询。
ALTER TABLE accounts
    ADD COLUMN IF NOT EXISTS disable_auto_temp_unschedulable BOOLEAN NOT NULL DEFAULT FALSE;

COMMENT ON COLUMN accounts.disable_auto_temp_unschedulable IS
    'Never auto-mark account as temporarily unschedulable when true';
