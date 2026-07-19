-- Seed data for db-query integration tests. Deliberately exercises the
-- parse edges: NULL vs empty string, and a value containing commas.
CREATE TABLE people (
    id       integer PRIMARY KEY,
    name     text NOT NULL,
    nickname text,
    note     text
);

INSERT INTO people (id, name, nickname, note) VALUES
    (1, 'Ada',    NULL,  'first programmer'),
    (2, 'Grace',  '',    'compiler pioneer'),
    (3, 'Edsger', 'EWD', 'structured, humble, precise');
