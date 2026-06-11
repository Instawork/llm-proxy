import type { FeatureToggle } from "../types";

export function featureEnabled(feature: boolean | FeatureToggle | undefined): boolean {
  if (typeof feature === "boolean") return feature;
  return Boolean(feature?.enabled);
}

export function featureMeta(feature: boolean | FeatureToggle | undefined): string {
  if (typeof feature === "boolean") return feature ? "enabled" : "disabled";
  if (!feature) return "unknown";
  return feature.backend ?? feature.mode ?? (feature.enabled ? "enabled" : "disabled");
}

export function featureLabel(name: string): string {
  return name.replaceAll("_", " ");
}
