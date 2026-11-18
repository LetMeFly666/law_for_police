CREATE DATABASE IF NOT EXISTS law_study
  CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- Replace the password before running this script.
CREATE USER IF NOT EXISTS 'lawstudy'@'localhost' IDENTIFIED BY 'REPLACE_WITH_LONG_RANDOM_PASSWORD';
GRANT SELECT, INSERT, UPDATE, DELETE, CREATE, ALTER, INDEX, REFERENCES
  ON law_study.* TO 'lawstudy'@'localhost';
FLUSH PRIVILEGES;
