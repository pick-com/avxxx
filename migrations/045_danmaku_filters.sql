-- 弹幕语言过滤与个人屏蔽词 (PRD 5.5 设置项: 语言过滤、屏蔽词).
-- Idempotent: every statement can run again on an up-to-date schema.

-- danmaku.lang (017) is now the detected language, one of
-- community.DanmakuLangs: zh ja ko en es vi th, or und for a body without
-- letters. POST .../danmaku sets it with community.DetectLang (script
-- heuristic, the client's lang only as a hint for Latin text). Older rows
-- (empty, or a client tag such as 'zh-CN') get the same rules here, without
-- the hint. The partial index keeps the re-run on every Open cheap: once
-- backfilled, no row matches it.
CREATE INDEX IF NOT EXISTS danmaku_lang_undetected ON danmaku (id)
  WHERE lang NOT IN ('zh','ja','ko','en','es','vi','th','und');
UPDATE danmaku SET lang = CASE
    WHEN body ~ '[ぁ-ゖゝ-ゟァ-ヺー-ヿㇰ-ㇿｦ-ﾝ]' THEN 'ja'
    WHEN body ~ '[ᄀ-ᇿ㄰-㆏가-힯]' THEN 'ko'
    WHEN body ~ '[ก-ฺเ-๛]' THEN 'th'
    WHEN body ~ '[㐀-䶿一-鿿豈-﫿]' THEN 'zh'
    WHEN body !~ '[A-Za-zªºÀ-ÖØ-öø-ɏḀ-ỿＡ-Ｚａ-ｚ]' THEN 'und'
    WHEN body ~ '[ăâđêôơưạảấầẩẫậắằẳẵặẹẻẽếềểễệỉịọỏốồổỗộớờởỡợụủứừửữựỳỵỷỹĂÂĐÊÔƠƯẠẢẤẦẨẪẬẮẰẲẴẶẸẺẼẾỀỂỄỆỈỊỌỎỐỒỔỖỘỚỜỞỠỢỤỦỨỪỬỮỰỲỴỶỸ]' THEN 'vi'
    WHEN body ~ '[ñÑ¿¡]' THEN 'es'
    ELSE 'en' END
  WHERE lang NOT IN ('zh','ja','ko','en','es','vi','th','und');

-- 个人屏蔽词: the viewer's own blocked words, applied in the player (never on
-- the server: other viewers still see those danmaku). Visitors keep the list
-- in localStorage; a logged-in user's list syncs through PATCH
-- /api/me/settings (store.ApplySettingsPatch: at most 100 words of 1-20
-- characters, deduplicated case-insensitively).
ALTER TABLE user_settings ADD COLUMN IF NOT EXISTS danmaku_block_words text[] NOT NULL DEFAULT '{}';
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'user_settings_danmaku_block_words_check') THEN
    ALTER TABLE user_settings ADD CONSTRAINT user_settings_danmaku_block_words_check
      CHECK (cardinality(danmaku_block_words) <= 100);
  END IF;
END
$$;
