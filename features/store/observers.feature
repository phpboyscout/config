@store @observers
Feature: Change notification
  Covers D9 (exactly-once notification, after the swap) and R2 (writing from
  inside an observer is refused). An observer hears about a change once per
  logical change, and reacting to one by writing another is a breakdown of the
  ownership model rather than a supported pattern.

  Background:
    Given a config file "/app.yaml" containing:
      """
      value: first
      count: 0
      """

  Scenario: One write touching two files notifies once
    Given a config file "/other.yaml" containing:
      """
      second: 0
      """
    And a store reading "/app.yaml" then "/other.yaml"
    And an observer that records what it is told
    When I set "count" to "1" and "second" to "2"
    Then the write succeeds
    And observers were notified 1 time

  Scenario: A write that changes nothing notifies nobody
    Given a store reading "/app.yaml"
    And an observer that records what it is told
    When I set "value" to "first"
    Then the write succeeds
    And observers were notified 0 times

  Scenario: An observer cannot write while reacting
    Given a store reading "/app.yaml"
    And an observer that writes "count" while reacting
    When I set "value" to "second"
    Then the write succeeds
    And observers were notified 1 time
    And the observer's write was refused

  Scenario: An observer may hand its write to another goroutine
    Given a store reading "/app.yaml"
    And an observer that defers its write to another goroutine
    When I set "value" to "second"
    Then the write succeeds
    And the deferred write succeeds
