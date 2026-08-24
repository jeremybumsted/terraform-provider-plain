terraform {
  required_providers {
    plain = {
      source = "jeremybumsted/plain"
    }
  }
}

provider "plain" {
  # Can also be supplied via the PLAIN_API_KEY environment variable.
  api_key = var.plain_api_key
}
