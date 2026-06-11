import {
  ArcElement,
  BarElement,
  CategoryScale,
  Chart as ChartJS,
  Filler,
  Legend,
  LinearScale,
  LineElement,
  PointElement,
  Tooltip,
} from "chart.js";

let registered = false;

export function ensureChartsRegistered() {
  if (registered) return;
  ChartJS.register(
    ArcElement,
    BarElement,
    CategoryScale,
    Filler,
    Legend,
    LinearScale,
    LineElement,
    PointElement,
    Tooltip,
  );
  ChartJS.defaults.font.family =
    "Inter, ui-sans-serif, system-ui, -apple-system, 'Segoe UI', sans-serif";
  ChartJS.defaults.color = cssVar("--color-base-content", "#1f2937", 0.7);
  registered = true;
}

/**
 * Reads a daisyUI/Tailwind CSS custom property off the document root so charts
 * follow the active theme. daisyUI v5 exposes OKLCH color tokens as
 * `--color-*`; we fall back to a literal when running outside the browser.
 */
export function cssVar(name: string, fallback: string, alpha = 1): string {
  if (typeof window === "undefined") return fallback;
  const raw = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  if (!raw) return fallback;
  if (alpha < 1) {
    // OKLCH values can be wrapped to apply alpha via color-mix.
    return `color-mix(in oklch, ${raw} ${Math.round(alpha * 100)}%, transparent)`;
  }
  return raw;
}

export const chartPalette = {
  primary: () => cssVar("--color-primary", "#6366f1"),
  primarySoft: () => cssVar("--color-primary", "#6366f1", 0.15),
  success: () => cssVar("--color-success", "#22c55e"),
  warning: () => cssVar("--color-warning", "#f59e0b"),
  error: () => cssVar("--color-error", "#ef4444"),
  info: () => cssVar("--color-info", "#0ea5e9"),
  grid: () => cssVar("--color-base-content", "#1f2937", 0.08),
  tick: () => cssVar("--color-base-content", "#1f2937", 0.6),
};
