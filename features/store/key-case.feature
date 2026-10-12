@store @key-case
Feature: Key case, folded by default and exact by choice
  Covers spec 0015. A Store folds keys to lower case unless it is built with
  WithCaseSensitiveKeys, and then every key is the key its source spells.

  Scenario: A folded store reads and writes a key in any case
    Given a config file "/app.yaml" containing:
      """
      Server:
        Port: 8080
      """
    And a store reading "/app.yaml"
    When I set "server.port" to "9090"
    Then the write succeeds
    And "SERVER.PORT" reads as "9090"
    And "/app.yaml" on disk contains:
      """
      Server:
        Port: "9090"
      """

  Scenario: A folded store refuses a file whose keys differ only in case
    Given a config file "/app.yaml" containing:
      """
      port: 8080
      Port: 9090
      """
    Then opening a store reading "/app.yaml" fails because two keys collide

  Scenario: A case-sensitive store keeps keys that differ only in case apart
    Given a config file "/app.yaml" containing:
      """
      port: 8080
      Port: 9090
      """
    And a case-sensitive store reading "/app.yaml"
    Then "port" reads as "8080"
    And "Port" reads as "9090"
    And "PORT" is not set

  Scenario: A case-sensitive store writes the key as it is spelled
    Given a config file "/app.yaml" containing:
      """
      Server:
        Port: 8080
      """
    And a case-sensitive store reading "/app.yaml"
    When I set "server.port" to "9090"
    Then the write succeeds
    And "Server.Port" reads as "8080"
    And "server.port" reads as "9090"
