-- SQLite schema for op-alt-da async architecture

CREATE TABLE IF NOT EXISTS blobs (
    -- Primary key (auto-increment enforces strict ordering)
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,

    -- Blob identification
    commitment          BLOB NOT NULL UNIQUE,
    namespace           BLOB NOT NULL,

    -- Blob data
    blob_data           BLOB NOT NULL,
    blob_size           INTEGER NOT NULL,

    -- Submission lifecycle
    status              TEXT NOT NULL CHECK(status IN (
                            'pending_submission',
                            'batched',
                            'submitted',
                            'confirmed',
                            'failed'
                        )),

    -- Timestamps
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    submitted_at        TIMESTAMP,
    confirmed_at        TIMESTAMP,

    -- Batch association
    batch_id            INTEGER,
    batch_index         INTEGER,  -- Position within batch (0-based)

    -- Celestia metadata
    celestia_height     INTEGER,

    -- Retry tracking
    retry_count         INTEGER NOT NULL DEFAULT 0,
    last_error          TEXT,
    next_retry_at       TIMESTAMP,

    -- Read tracking
    read_status         TEXT NOT NULL DEFAULT 'unread' CHECK(read_status IN ('unread', 'read')),
    last_read_at        TIMESTAMP,
    read_count          INTEGER NOT NULL DEFAULT 0,

    FOREIGN KEY (batch_id) REFERENCES batches(batch_id)
);

CREATE TABLE IF NOT EXISTS batches (
    -- Primary key
    batch_id            INTEGER PRIMARY KEY AUTOINCREMENT,

    -- Batch commitment (computed from packed blob data)
    batch_commitment    BLOB NOT NULL UNIQUE,

    -- Batch data (packed individual blobs)
    batch_data          BLOB NOT NULL,
    batch_size          INTEGER NOT NULL,
    blob_count          INTEGER NOT NULL,

    -- Celestia metadata
    celestia_height     INTEGER,
    celestia_tx_hash    TEXT,

    -- Status
    status              TEXT NOT NULL CHECK(status IN ('submitted', 'confirmed', 'failed')),

    -- Timestamps
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    submitted_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    confirmed_at        TIMESTAMP,

    -- Retry tracking
    retry_count         INTEGER NOT NULL DEFAULT 0,
    last_error          TEXT
);

-- Indexes for fast queries
CREATE INDEX IF NOT EXISTS idx_blobs_status ON blobs(status, id);
CREATE INDEX IF NOT EXISTS idx_blobs_commitment ON blobs(commitment);
CREATE INDEX IF NOT EXISTS idx_blobs_created_at ON blobs(created_at);
CREATE INDEX IF NOT EXISTS idx_blobs_batch_id ON blobs(batch_id);
CREATE INDEX IF NOT EXISTS idx_blobs_next_retry ON blobs(next_retry_at) WHERE status = 'pending_submission';

CREATE INDEX IF NOT EXISTS idx_batches_status ON batches(status, batch_id);
CREATE INDEX IF NOT EXISTS idx_batches_commitment ON batches(batch_commitment);
CREATE INDEX IF NOT EXISTS idx_batches_height ON batches(celestia_height);
