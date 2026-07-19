@store @reload
Feature: Reload lifecycle
  Covers D9. A reload is fail-closed: a configuration the application has said
  it cannot use never becomes live, and the previous one stands. A rejection is
  not a change, so it travels on its own channel rather than being announced to
  observers as though something happened.

  Background:
    Given a schema requiring "server.host" to be a string

  Scenario: A rejected reload keeps the last known good configuration
    Given a config file "/app.yaml" containing:
      """
      server:
        host: good-host
        port: 8080
      """
    And a store reading "/app.yaml" with that schema
    And the reload errors are being collected
    And an observer that records what it is told
    When "/app.yaml" is changed on disk to:
      """
      server:
        port: 9090
      """
    And the store reloads
    Then the reload is rejected
    And "server.host" reads as "good-host"
    And the rejection reached the error channel

  Scenario: A rejected reload notifies no observer
    Given a config file "/app.yaml" containing:
      """
      server:
        host: good-host
      """
    And a store reading "/app.yaml" with that schema
    And the reload errors are being collected
    And an observer that records what it is told
    When "/app.yaml" is changed on disk to:
      """
      server:
        port: 1
      """
    And the store reloads
    Then the reload is rejected
    And observers were notified 0 times

  Scenario: An accepted reload publishes and notifies once
    Given a config file "/app.yaml" containing:
      """
      server:
        host: first
      """
    And a store reading "/app.yaml" with that schema
    And the reload errors are being collected
    And an observer that records what it is told
    When "/app.yaml" is changed on disk to:
      """
      server:
        host: second
      """
    And the store reloads
    Then the reload succeeds
    And "server.host" reads as "second"
    And observers were notified 1 time

  Scenario: A reload that changes nothing notifies nobody
    Given a config file "/app.yaml" containing:
      """
      server:
        host: same
      """
    And a store reading "/app.yaml" with that schema
    And the reload errors are being collected
    And an observer that records what it is told
    When "/app.yaml" is changed on disk to:
      """
      server:
        host: same
      """
    And the store reloads
    Then the reload succeeds
    And observers were notified 0 times
