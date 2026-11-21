import { defineConfig, globalIgnores } from "eslint/config";
import nextVitals from "eslint-config-next/core-web-vitals";
import nextTypescript from "eslint-config-next/typescript";

export default defineConfig([
  ...nextVitals,
  ...nextTypescript,
  {
    rules: {
      "jsx-a11y/no-redundant-roles": "error",
      "jsx-a11y/role-has-required-aria-props": "error",
      "jsx-a11y/aria-activedescendant-has-tabindex": "error",
      "jsx-a11y/label-has-associated-control": "error",
      "jsx-a11y/tabindex-no-positive": "error",
      "jsx-a11y/heading-has-content": "error",
      "jsx-a11y/anchor-has-content": "error",
      "jsx-a11y/html-has-lang": "error",
      "jsx-a11y/iframe-has-title": "error",
    },
  },
  {
    files: ["app/**/virtual.tsx"],
    rules: { "react-hooks/incompatible-library": "off" },
  },
  globalIgnores([".next/**", "next-env.d.ts"]),
]);
