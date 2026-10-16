-- Store only sha256(token) for sessions. Existing plaintext tokens are hashed in
-- place and the column is renamed to token_hash; the rename is the marker that
-- makes this idempotent (re-running never hashes a hash twice).
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = current_schema() AND table_name = 'user_sessions' AND column_name = 'token'
  ) AND NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = current_schema() AND table_name = 'user_sessions' AND column_name = 'token_hash'
  ) THEN
    UPDATE user_sessions SET token = encode(sha256(convert_to(token, 'UTF8')), 'hex');
    ALTER TABLE user_sessions RENAME COLUMN token TO token_hash;
  END IF;
END
$$;

-- Passwords: no schema change. users.password_hash now holds argon2id PHC strings;
-- legacy 64-hex SHA-256 values are upgraded transparently on the next successful login.
