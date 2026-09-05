CREATE TABLE logs (
    event_id String DEFAULT toString(generateUUIDv4()),
    id Int64 DEFAULT 0,
    user_id Int32 DEFAULT 0,
    created_at Int64 DEFAULT 0,
    type Int32 DEFAULT 0,
    content String DEFAULT '',
    username String DEFAULT '',
    token_name String DEFAULT '',
    model_name String DEFAULT '',
    quota Int32 DEFAULT 0,
    prompt_tokens Int32 DEFAULT 0,
    completion_tokens Int32 DEFAULT 0,
    use_time Int32 DEFAULT 0,
    is_stream UInt8 DEFAULT 0,
    channel_id Int32 DEFAULT 0,
    token_id Int32 DEFAULT 0,
    `group` String DEFAULT '',
    ip String DEFAULT '',
    request_id String DEFAULT '',
    upstream_request_id String DEFAULT '',
    other String DEFAULT ''
)
ENGINE = MergeTree()
PARTITION BY toYYYYMM(toDateTime(created_at))
ORDER BY (created_at, request_id, event_id);
