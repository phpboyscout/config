@store @closing
Feature: Closing a store
  Covers spec 0013. A store releases what it was handed when it closes, and
  never leaves a watch running against a connection it has released. It keeps
  answering reads from the configuration it last held, and refuses anything
  that would load, write or watch.

  Background:
    Given a config file "/app.yaml" containing:
      """
      value: first
      """
    And a store reading "/app.yaml" that holds a connection
    And the store is watching

  Scenario: Closing releases the watch and the connection it was handed
    When the store is closed
    Then the watcher was released
    And the connection was released
    And "value" reads as "first"

  Scenario: A closed store refuses to change
    When the store is closed
    And the store reloads
    Then the store refuses because it is closed
    When I set "value" to "second"
    Then the store refuses because it is closed
    And "value" reads as "first"
