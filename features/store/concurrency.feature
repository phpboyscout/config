@store @concurrency
Feature: Concurrent writes and conflict detection
  Covers D3 and D14. The Store serialises access internally, so callers need no
  protocol between themselves. A change made behind its back is refused rather
  than silently overwritten, because discarding someone else's work without
  saying so is the one outcome a configuration writer must never produce.

  Scenario: Concurrent writes do not lose updates
    Given a config file "/app.yaml" containing:
      """
      existing: yes
      """
    And a store reading "/app.yaml"
    When 8 writers set their own key at once
    Then every write succeeded
    And "existing" reads as "yes"

  Scenario: A change made behind the store's back is refused
    Given a config file "/app.yaml" containing:
      """
      value: original
      """
    And a store reading "/app.yaml"
    When "/app.yaml" is changed behind the store's back to:
      """
      value: someone-else
      intruder: yes
      """
    And I set "value" to "mine"
    Then the write is refused as a conflict
    And "/app.yaml" on disk contains:
      """
      intruder: yes
      """
    And "/app.yaml" on disk does not contain "mine"

  Scenario: The store recovers once the foreign change is adopted
    Given a config file "/app.yaml" containing:
      """
      value: original
      """
    And a store reading "/app.yaml"
    When "/app.yaml" is changed behind the store's back to:
      """
      value: someone-else
      """
    And the store reloads
    And I set "value" to "mine"
    Then the write succeeds
    And "value" reads as "mine"
