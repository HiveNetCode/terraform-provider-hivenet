terraform {
  required_providers {
    hivenet = {
      source = "hivenetcode/hivenet"
    }
  }
}

# The API key can also come from HIVENET_TOKEN.
provider "hivenet" {
  token = var.hivenet_token
}

variable "hivenet_token" {
  type      = string
  sensitive = true
}
