import { useMemo } from "react";
import { PrismLight as SyntaxHighlighter } from "react-syntax-highlighter";
import bash from "react-syntax-highlighter/dist/esm/languages/prism/bash";
import go from "react-syntax-highlighter/dist/esm/languages/prism/go";
import python from "react-syntax-highlighter/dist/esm/languages/prism/python";
import typescript from "react-syntax-highlighter/dist/esm/languages/prism/typescript";
import { oneDark } from "react-syntax-highlighter/dist/esm/styles/prism";

SyntaxHighlighter.registerLanguage("python", python);
SyntaxHighlighter.registerLanguage("typescript", typescript);
SyntaxHighlighter.registerLanguage("go", go);
SyntaxHighlighter.registerLanguage("bash", bash);

const LANG_ALIASES: Record<string, string> = {
  javascript: "typescript",
  jsx: "typescript",
  ts: "typescript",
  tsx: "typescript",
  shell: "bash",
  sh: "bash",
};

function resolveLanguage(language: string): string {
  const key = language.trim().toLowerCase();
  return LANG_ALIASES[key] ?? key;
}

export function CodeBlock({ code, language }: { code: string; language: string }) {
  const lang = useMemo(() => resolveLanguage(language), [language]);

  return (
    <SyntaxHighlighter
      language={lang}
      style={oneDark}
      PreTag="div"
      customStyle={{
        margin: 0,
        padding: "1rem 1.25rem",
        background: "transparent",
        fontSize: "0.875rem",
        lineHeight: 1.6,
      }}
      codeTagProps={{
        style: {
          fontFamily: "ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace",
        },
      }}
    >
      {code}
    </SyntaxHighlighter>
  );
}
