import logo from "../assets/llm-proxy-logo.png";

interface LLMProxyLogoProps {
  size?: "sm" | "md" | "lg";
  className?: string;
}

const sizeClass = {
  sm: "h-9 w-9",
  md: "h-11 w-11",
  lg: "h-14 w-14",
} as const;

export default function LLMProxyLogo({ size = "md", className = "" }: LLMProxyLogoProps) {
  return (
    <img
      src={logo}
      alt="LLM Proxy"
      className={`rounded-2xl object-cover shadow-md ${sizeClass[size]} ${className}`.trim()}
    />
  );
}
