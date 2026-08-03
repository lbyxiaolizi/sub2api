-- 修复 user_platform_quotas 平台 CHECK 约束：同时允许 grok 与 kiro。
--
-- 背景：
--   - 157 官方版约束为 ('anthropic','openai','gemini','antigravity','grok')；
--   - 线上数据库（由早期镜像初始化）实际约束为 ('anthropic','openai','gemini',
--     'antigravity','kiro')——缺少 grok；若注册 grok 账号，自助注册时
--     snapshotPlatformQuotaDefaults 写入 grok 行会违反 CHECK，导致注册事务
--     aborted（同 157 注释描述的问题路径）。
--   - fork 曾直接改写已应用的 157 来加 grok/kiro，造成 checksum 漂移；
--     按迁移不可变性原则，这里不改 157，改用新的 migration 对齐代码平台列表。
--
-- DROP ... IF EXISTS 保证可重入；新约束是旧约束的超集，存量行瞬时校验通过。
ALTER TABLE user_platform_quotas
    DROP CONSTRAINT IF EXISTS user_platform_quotas_platform_check;

ALTER TABLE user_platform_quotas
    ADD CONSTRAINT user_platform_quotas_platform_check
    CHECK (platform IN ('anthropic', 'openai', 'gemini', 'antigravity', 'grok', 'kiro'));
