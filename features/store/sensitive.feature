@store @writes @sensitive
Feature: Secrets are never written into a plainer layer
  Covers D5 of the dynamic backend adapters spec. A backend over a secrets
  manager declares itself sensitive and is read-only, so a write to a key it
  provides cannot land there — routing falls through to the next writable layer,
  which is typically a plain file on disk. Without a guard that is how a password
  ends up in config.yaml, so the write is refused instead.

  The guard keys off sensitivity, not read-only-ness, and it lets a removal
  through, because removing a key writes no secret anywhere.

  Background:
    Given a config file "/app.yaml" containing:
      """
      db:
        password: from-file
        host: db.internal
      """

  Scenario: A secret is not written into the plain file beneath it
    Given a sensitive read-only layer providing "db.password" as "s3cr3t"
    And a store reading "/app.yaml" beneath that layer
    When I set "db.password" to "rotated"
    Then the write is refused as a sensitive leak
    And "/app.yaml" on disk does not contain "rotated"
    And "/app.yaml" on disk does not contain "s3cr3t"

  Scenario: A key the sensitive layer does not provide is written as usual
    Given a sensitive read-only layer providing "db.password" as "s3cr3t"
    And a store reading "/app.yaml" beneath that layer
    When I set "db.host" to "db.example.com"
    Then the write succeeds
    And "/app.yaml" on disk contains:
      """
      host: db.example.com
      """

  Scenario: Removing a secret-provided key is allowed
    Given a sensitive read-only layer providing "db.password" as "s3cr3t"
    And a store reading "/app.yaml" beneath that layer
    When I remove "db.password"
    Then the write succeeds
    And "/app.yaml" on disk does not contain "from-file"
    And "/app.yaml" on disk does not contain "s3cr3t"

  Scenario: A read-only layer that is not sensitive routes the write beneath
    Given a read-only layer providing "db.password" as "from-remote"
    And a store reading "/app.yaml" beneath that layer
    When I set "db.password" to "rotated"
    Then the write succeeds
    And "/app.yaml" on disk contains:
      """
      password: rotated
      """
