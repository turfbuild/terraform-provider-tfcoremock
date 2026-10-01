provider "tfcoremock" {
  defer_on_read = ["simple_resource"]
}

data "tfcoremock_simple_resource" "data" {
  id = "simple_resource"
}

resource "tfcoremock_simple_resource" "test" {
  integer = data.tfcoremock_simple_resource.data.integer
}
