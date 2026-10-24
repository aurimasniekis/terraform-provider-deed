# By entity ID …
terraform import deed_entity.nas ent_01m5qxagfserct3h3garhw4k71

# … or by natural key: <project>/<kind>/<name> (the kind may contain a slash, e.g. homelab/example.com/ups/ups-01).
terraform import deed_entity.nas homelab/server/nas-01
