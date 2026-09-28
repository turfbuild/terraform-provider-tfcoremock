provider "tfcoremock" {
  warn_on_invoke = ["warned"]
}

resource "tfcoremock_simple_resource" "resource" {
  lifecycle {
    action_trigger {
      events  = [before_create]
      actions = [action.tfcoremock_simple_resource.action]
    }
  }
}

action "tfcoremock_simple_resource" "action" {
  config {
    string = "warned"
  }
}
