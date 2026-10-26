-- the key itself, so /key can show the same key again instead of issuing a new one;
-- it unlocks only data that lives in this same database
alter table api_keys add column key_plain text;
