data "hivenet_presets" "rtx4090_fr" {
  region           = "FR"
  free_text_search = "RTX 4090"
}

# Cheapest matching preset, using the discounted price when there is one.
locals {
  prices = {
    for p in data.hivenet_presets.rtx4090_fr.presets :
    p.id => coalesce(p.price_discounted, p.hourly_price)
  }
  cheapest_preset_id = [for id, price in local.prices : id if price == min(values(local.prices)...)][0]
}
