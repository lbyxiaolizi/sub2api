-- 代理池修复与账号池绑定：
--
-- 1) 清理历史软删除的代理池行。
--    193 建表后删除池走的是 ent SoftDeleteMixin（UPDATE deleted_at），已删行的
--    name 仍占用 UNIQUE 约束，导致「删除后重建同名池」违反唯一约束（500）。
--    服务端 DeletePool 已改为硬删（原生 SQL）；这里把存量软删行一并清理，
--    保证同名池可重建。proxy_pool_rebind_logs.pool_id 为 ON DELETE CASCADE，
--    proxies.pool_id 为 ON DELETE SET NULL，硬删安全。
DELETE FROM proxy_pools WHERE deleted_at IS NOT NULL;

-- 2) accounts 增加可选代理池绑定（pool_id）。
--    账号绑定池后，池服务会为其在池内健康代理间维护分配（写入 accounts.proxy_id）；
--    绑定具体代理则 pool_id 为 NULL。池删除时绑定自动置空。
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS pool_id BIGINT REFERENCES proxy_pools(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_accounts_pool_id ON accounts(pool_id);
