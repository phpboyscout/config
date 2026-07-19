@store @writes
Feature: Layer-correct, comment-preserving writes
  Covers D13 (edit routing), D14 (plan/apply phases) and D4 (own writes build
  the next snapshot directly). A write targets the layer that owns the key,
  leaves every other layer alone, and never flattens the merged view into the
  file it happens to be writing.

  Scenario: A write preserves the comments around the key it changes
    Given a config file "/app.yaml" containing:
      """
      # How the server is exposed.
      server:
        # The port to bind.
        port: 8080 # inline note
        host: localhost
      """
    And a store reading "/app.yaml"
    When I set "server.port" to "9090"
    Then the write succeeds
    And "server.port" reads as "9090"
    And "/app.yaml" on disk contains:
      """
      # How the server is exposed.
      # The port to bind.
      host: localhost
      """

  Scenario: A write targets the layer that already defines the key
    Given a config file "/base.yaml" containing:
      """
      shared: from-base
      onlybase: yes
      """
    And a config file "/over.yaml" containing:
      """
      shared: from-overlay
      """
    And a store reading "/base.yaml" then "/over.yaml"
    When I set "onlybase" to "changed"
    Then the write succeeds
    And "onlybase" comes from "/base.yaml"
    And "/over.yaml" on disk does not contain "onlybase"

  Scenario: A write does not flatten values resolved from another layer
    Given a config file "/base.yaml" containing:
      """
      shared: from-base
      inherited: from-base
      """
    And a config file "/over.yaml" containing:
      """
      shared: from-overlay
      """
    And a store reading "/base.yaml" then "/over.yaml"
    When I set "shared" to "written"
    Then the write succeeds
    And "/over.yaml" on disk does not contain "inherited"

  Scenario: Removing a key takes its subtree with it
    Given a config file "/app.yaml" containing:
      """
      keep: yes
      drop:
        a: 1
        b: 2
      """
    And a store reading "/app.yaml"
    When I remove "drop"
    Then the write succeeds
    And "/app.yaml" on disk does not contain "drop"
    And "/app.yaml" on disk contains:
      """
      keep: yes
      """
