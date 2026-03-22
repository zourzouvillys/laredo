CREATE TABLE IF NOT EXISTS test_users (
    id SERIAL PRIMARY KEY,
    name TEXT NOT NULL,
    email TEXT,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

ALTER TABLE test_users REPLICA IDENTITY FULL;

INSERT INTO test_users (name, email) VALUES
    ('alice', 'alice@example.com'),
    ('bob', 'bob@example.com'),
    ('charlie', 'charlie@example.com');
