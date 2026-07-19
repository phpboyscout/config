@store @typed-sections
Feature: Typed configuration sections
  Covers D10, the module's extraction mechanism and a frozen contract. A package
  declares a local interface over a settings struct and never imports config at
  all; the binding here is what keeps that struct current across a reload.

  Background:
    Given a config file "/app.yaml" containing:
      """
      server:
        host: first-host
        port: 8080
      """
    And a store reading "/app.yaml"
    And a typed section bound to "server"

  Scenario: A section hydrates from the configuration it was bound to
    Then the section exists
    And the section reads host "first-host" and port 8080

  Scenario: A section rehydrates after a change
    When I set "server.host" to "second-host"
    Then the section reads host "second-host" and port 8080

  Scenario: A write elsewhere does not disturb the section
    When I set "unrelated" to "value"
    Then the section reads host "first-host" and port 8080

  Scenario: The version starts at one and moves only on a real change
    Then the section version is 1

  Scenario: A reload that changes nothing leaves the version alone
    When "/app.yaml" is changed on disk to:
      """
      server:
        host: first-host
        port: 8080
      """
    And the store reloads
    Then the reload succeeds
    And the section version is 1
    And the section reads host "first-host" and port 8080

  Scenario: A change that touches the section moves the version on
    When I set "server.host" to "second-host"
    Then the section version is 2
    And the section reads host "second-host" and port 8080
