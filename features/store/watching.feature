@store @watching
Feature: Watching for foreign changes
  Covers D8 and D9. Watching exists to notice changes the Store did not make.
  Its own writes build the next snapshot directly, so they never return through
  the watcher. Filesystem notification is noisy, so an event is a hint that
  something may have changed rather than a statement that it did.

  Background:
    Given a config file "/app.yaml" containing:
      """
      value: first
      """
    And a store reading "/app.yaml"
    And an observer that records what it is told
    And the store is watching

  Scenario: A foreign change is picked up and announced once
    When "/app.yaml" is changed on disk to:
      """
      value: second
      """
    And a change is reported
    Then "value" reads as "second"
    And observers were notified 1 time

  Scenario: Events that change nothing are ignored
    When a change is reported
    And a change is reported
    And a change is reported
    Then observers were notified 0 times

  Scenario: Rewriting identical content is not a change
    When "/app.yaml" is changed on disk to:
      """
      value: first
      """
    And a change is reported
    Then observers were notified 0 times

  Scenario: The store's own write does not come back round
    When I set "value" to "written"
    And a change is reported
    Then observers were notified 1 time
    And "value" reads as "written"

  Scenario: Stopping releases the watcher
    When watching stops
    Then the watcher was released
