-- Compliance (PRD 15; tech plan 3.13; CookieBanner / LegalPage /
-- CopyrightForm): server-side consent records, versioned legal documents,
-- feedback, and copyright takedown tickets with counter-notices. Re-run on
-- every start: every statement is idempotent. (036–039 belong to other work.)

-- ---------------------------------------------------------------- consent

-- Append-only log of cookie consent choices. Who: the logged-in user, or a
-- keyed hash of a random per-browser id (never the raw id, never an IP).
-- categories is the PRD 14.1 list (necessary always; analytics,
-- personalized_ads, personalized_recs when granted). policy_version is the
-- published cookies document version in force when the choice was saved.
-- A Global Privacy Control signal always means personalized_ads off.
CREATE TABLE IF NOT EXISTS consent_records (
  id bigserial PRIMARY KEY,
  user_id bigint REFERENCES users(id) ON DELETE CASCADE,
  device_hash text,
  categories text[] NOT NULL,
  policy_version integer NOT NULL DEFAULT 0,
  gpc boolean NOT NULL DEFAULT false,
  source text NOT NULL CHECK (source IN ('banner', 'preferences', 'sync', 'gpc')),
  created_at timestamptz NOT NULL DEFAULT now(),
  CHECK (user_id IS NOT NULL OR device_hash IS NOT NULL),
  CHECK ('necessary' = ANY (categories)),
  CHECK (categories <@ ARRAY['necessary', 'analytics', 'personalized_ads', 'personalized_recs']::text[]),
  CHECK (NOT (gpc AND 'personalized_ads' = ANY (categories)))
);
CREATE INDEX IF NOT EXISTS consent_records_user ON consent_records (user_id, created_at DESC, id DESC) WHERE user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS consent_records_device ON consent_records (device_hash, created_at DESC) WHERE device_hash IS NOT NULL;

-- Corrections are new rows; rows are only removed with the account (cascade).
CREATE OR REPLACE FUNCTION consent_records_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'consent_records is append-only';
END $$;
DROP TRIGGER IF EXISTS consent_records_no_update ON consent_records;
CREATE TRIGGER consent_records_no_update BEFORE UPDATE ON consent_records
  FOR EACH ROW EXECUTE FUNCTION consent_records_append_only();

-- ---------------------------------------------------------------- legal documents

-- One row per (kind, language, version). A version number belongs to the
-- kind: version 3 of the terms in en is the translation of version 3 in
-- zh-CN. Drafts are editable; a published row is frozen (only a new version
-- changes the text). The current version of a kind is the highest published
-- version whose effective_at has passed.
CREATE TABLE IF NOT EXISTS legal_documents (
  id bigserial PRIMARY KEY,
  kind text NOT NULL CHECK (kind IN ('terms', 'privacy', 'cookies', 'copyright', 'community', 'ads', 'performer-info')),
  lang text NOT NULL,
  version integer NOT NULL CHECK (version > 0),
  title text NOT NULL,
  summary text[] NOT NULL DEFAULT '{}',
  body text NOT NULL, -- Markdown (## heading {#anchor}, paragraphs, - lists)
  status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'published')),
  effective_at timestamptz,
  published_at timestamptz,
  published_by text,
  change_note text NOT NULL DEFAULT '',
  created_by text NOT NULL DEFAULT 'admin',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (kind, lang, version),
  CHECK (status = 'draft' OR (effective_at IS NOT NULL AND published_at IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS legal_documents_current ON legal_documents (kind, version DESC) WHERE status = 'published';

CREATE OR REPLACE FUNCTION legal_documents_frozen() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    IF OLD.status = 'published' THEN
      RAISE EXCEPTION 'published legal documents cannot be deleted';
    END IF;
    RETURN OLD;
  END IF;
  IF OLD.status = 'published' THEN
    RAISE EXCEPTION 'published legal documents are frozen; draft a new version';
  END IF;
  RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS legal_documents_frozen ON legal_documents;
CREATE TRIGGER legal_documents_frozen BEFORE UPDATE OR DELETE ON legal_documents
  FOR EACH ROW EXECUTE FUNCTION legal_documents_frozen();

-- Version 1: the template texts that were hard-coded in web/app/lib/legal.ts,
-- still marked [待律师审阅]. Effective from the date of this migration.
INSERT INTO legal_documents (kind, lang, version, title, summary, body, status, effective_at, published_at, published_by, change_note, created_by)
VALUES
('privacy', 'zh-CN', 1, '隐私政策',
 ARRAY['不登录也能看；登录后我们保存观看记录、关注和设置，用来同步和推荐。',
       '分析和个性化广告需要你同意，随时可以在「Cookie 设置」里修改。',
       '浏览器发出的全局隐私控制（GPC）信号会被视为退出个性化广告。'],
 $md$## 我们收集哪些信息 {#collect}

[待律师审阅] 账号信息（邮箱、昵称）；观看记录；你主动提供的内容；设备与网络信息。具体范围以法律文本草稿为准。

## 我们怎么使用 {#use}

[待律师审阅] 本节正文尚未定稿，以法律文本草稿为准。

## 与谁分享 {#share}

[待律师审阅] 本节正文尚未定稿，以法律文本草稿为准。

## 你的权利 {#rights}

[待律师审阅] 在「设置 › 隐私与数据」里可以暂停或清除观看记录。查看、导出、更正和删除数据的流程以法律文本草稿为准。

## 儿童与青少年 {#children}

[待律师审阅] 本服务不面向 13 岁以下的儿童，注册时需要确认年龄。

## 保存多久 {#retention}

[待律师审阅] 本节正文尚未定稿，以法律文本草稿为准。

## 联系我们 {#contact}

[待律师审阅] 联系方式待定。
$md$, 'published', '2026-10-16T00:00:00Z', '2026-10-16T00:00:00Z', 'migration-041', '从前端模板迁移（lib/legal.ts）', 'migration-041'),
('terms', 'zh-CN', 1, '用户协议',
 ARRAY['使用本服务即表示同意本协议（定稿前以法律文本草稿为准）。'],
 $md$## 账号规则 {#account}

[待律师审阅] 本节正文尚未定稿，以法律文本草稿为准。

## 使用规范 {#conduct}

[待律师审阅] 本节正文尚未定稿，以法律文本草稿为准。

## 免责声明 {#disclaimer}

[待律师审阅] 本节正文尚未定稿，以法律文本草稿为准。

## 争议解决 {#disputes}

[待律师审阅] 本节正文尚未定稿，以法律文本草稿为准。
$md$, 'published', '2026-10-16T00:00:00Z', '2026-10-16T00:00:00Z', 'migration-041', '从前端模板迁移（lib/legal.ts）', 'migration-041'),
('cookies', 'zh-CN', 1, 'Cookie 政策',
 ARRAY['必要 Cookie 始终开启；分析、个性化广告、个性化推荐需要你同意。',
       '你的选择保存在当前浏览器里，可以随时在页脚「Cookie 设置」修改。'],
 $md$## Cookie 类别 {#categories}

[待律师审阅] 本节正文尚未定稿，以法律文本草稿为准。

## 你的选择 {#choices}

[待律师审阅] 本节正文尚未定稿，以法律文本草稿为准。

## 全局隐私控制（GPC） {#gpc}

[待律师审阅] 本节正文尚未定稿，以法律文本草稿为准。

## 第三方 {#third-parties}

[待律师审阅] 本节正文尚未定稿，以法律文本草稿为准。
$md$, 'published', '2026-10-16T00:00:00Z', '2026-10-16T00:00:00Z', 'migration-041', '从前端模板迁移（lib/legal.ts）', 'migration-041'),
('copyright', 'zh-CN', 1, '版权政策',
 ARRAY['收到版权投诉后会暂时下架相关内容并核对。'],
 $md$## 投诉方式 {#notice}

[待律师审阅] 本节正文尚未定稿，以法律文本草稿为准。

## 指定代理人 {#agent}

[待律师审阅] 本节正文尚未定稿，以法律文本草稿为准。

## 反通知 {#counter}

[待律师审阅] 本节正文尚未定稿，以法律文本草稿为准。
$md$, 'published', '2026-10-16T00:00:00Z', '2026-10-16T00:00:00Z', 'migration-041', '从前端模板迁移（lib/legal.ts）', 'migration-041'),
('community', 'zh-CN', 1, '社区规则',
 ARRAY['评论、弹幕、短评的发布规范。'],
 $md$## 基本规则 {#rules}

[待律师审阅] 本节正文尚未定稿，以法律文本草稿为准。

## 处理方式 {#enforcement}

[待律师审阅] 本节正文尚未定稿，以法律文本草稿为准。
$md$, 'published', '2026-10-16T00:00:00Z', '2026-10-16T00:00:00Z', 'migration-041', '从前端模板迁移（lib/legal.ts）', 'migration-041'),
('ads', 'zh-CN', 1, '广告说明',
 ARRAY['个性化广告需要你同意；关闭后广告数量不变。'],
 $md$## 广告怎么选 {#how}

[待律师审阅] 本节正文尚未定稿，以法律文本草稿为准。

## 你的选择 {#choices}

[待律师审阅] 本节正文尚未定稿，以法律文本草稿为准。
$md$, 'published', '2026-10-16T00:00:00Z', '2026-10-16T00:00:00Z', 'migration-041', '从前端模板迁移（lib/legal.ts）', 'migration-041'),
('performer-info', 'zh-CN', 1, '演员信息更正与隐藏',
 ARRAY['演员本人或经纪方可以申请更正或隐藏个人信息。'],
 $md$## 适用范围 {#scope}

[待律师审阅] 本节正文尚未定稿，以法律文本草稿为准。

## 申请方式 {#request}

[待律师审阅] 本节正文尚未定稿，以法律文本草稿为准。

## 隐藏后的效果 {#effect}

[待律师审阅] 本节正文尚未定稿，以法律文本草稿为准。
$md$, 'published', '2026-10-16T00:00:00Z', '2026-10-16T00:00:00Z', 'migration-041', '从前端模板迁移（lib/legal.ts）', 'migration-041')
ON CONFLICT (kind, lang, version) DO NOTHING;

-- ---------------------------------------------------------------- feedback

-- 反馈问题 / 帮助 / 合作 (LegalPage footer). email and page_url are optional
-- and admin-only; nothing here is ever returned by a public route.
CREATE TABLE IF NOT EXISTS feedback (
  id bigserial PRIMARY KEY,
  category text NOT NULL CHECK (category IN ('bug', 'playback', 'content', 'account', 'suggestion', 'partnership', 'other')),
  message text NOT NULL CHECK (length(message) BETWEEN 1 AND 4000),
  email text,
  page_url text,
  lang text,
  status text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'in_progress', 'resolved', 'dismissed')),
  admin_note text NOT NULL DEFAULT '',
  handled_by text,
  handled_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS feedback_queue ON feedback (status, created_at DESC, id DESC);

-- ---------------------------------------------------------------- takedowns

-- Copyright notices (CopyrightForm; PRD 15: temporary takedown within 24 h).
-- ticket is random (80 bits) and is the only public handle; the public
-- status lookup also needs the claimant's or counter-notifier's email.
CREATE TABLE IF NOT EXISTS takedown_requests (
  id bigserial PRIMARY KEY,
  ticket text NOT NULL UNIQUE,
  kind text NOT NULL DEFAULT 'copyright' CHECK (kind IN ('copyright')),
  status text NOT NULL DEFAULT 'received'
    CHECK (status IN ('received', 'reviewing', 'actioned', 'rejected', 'counter_received', 'restored', 'closed')),
  claimant_name text NOT NULL,
  claimant_email text NOT NULL,
  claimant_phone text NOT NULL DEFAULT '',
  claimant_address text NOT NULL DEFAULT '',
  rights_owner text NOT NULL,
  relationship text NOT NULL CHECK (relationship IN ('owner', 'agent')),
  work_description text NOT NULL,
  original_urls text[] NOT NULL DEFAULT '{}',
  infringing_urls text[] NOT NULL CHECK (cardinality(infringing_urls) BETWEEN 1 AND 20),
  good_faith boolean NOT NULL CHECK (good_faith),
  accuracy boolean NOT NULL CHECK (accuracy),
  signature text NOT NULL,
  lang text,
  due_at timestamptz NOT NULL,
  first_action_at timestamptz,
  counter_name text,
  counter_email text,
  counter_address text,
  counter_statement text,
  counter_consent boolean,
  counter_signature text,
  counter_received_at timestamptz,
  resolution_note text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS takedown_requests_queue ON takedown_requests (status, due_at, id);

-- What was done for a ticket, through the existing paths: a release moved
-- to unpublished (TransitionRelease) or an image suppressed
-- (SuppressMediaAsset, purged after 24 h). state 'restored' after a
-- counter-notice or a rejected claim.
CREATE TABLE IF NOT EXISTS takedown_targets (
  id bigserial PRIMARY KEY,
  takedown_id bigint NOT NULL REFERENCES takedown_requests(id) ON DELETE CASCADE,
  target_type text NOT NULL CHECK (target_type IN ('release', 'media_asset')),
  target_id bigint NOT NULL,
  action text NOT NULL CHECK (action IN ('unpublish', 'suppress')),
  previous_status text,
  detail jsonb NOT NULL DEFAULT '{}'::jsonb, -- release slug / title, or the image's entity, type and object key
  state text NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'restored')),
  actor text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  restored_at timestamptz,
  restored_by text
);
CREATE INDEX IF NOT EXISTS takedown_targets_ticket ON takedown_targets (takedown_id);
CREATE INDEX IF NOT EXISTS takedown_targets_active ON takedown_targets (target_type, target_id) WHERE state = 'active';
