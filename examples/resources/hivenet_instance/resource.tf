# Import the instance first; see Import below.
resource "hivenet_instance" "web" {
  power_state = "stopped"

  lifecycle {
    # Destroying this resource terminates the instance and deletes its disk.
    prevent_destroy = true
  }
}
