import { createContext, useContext, useEffect, useState } from "react";
import type { ReactNode } from "react";
import { getFeatures } from "./api-calls";
import type { Features } from "./types";

const defaultFeatures: Features = { telegram_bot: true };

const FeaturesContext = createContext<Features>(defaultFeatures);

export function FeaturesProvider({ children }: { children: ReactNode }) {
  const [features, setFeatures] = useState<Features>(defaultFeatures);

  useEffect(() => {
    let cancelled = false;
    getFeatures()
      .then((f) => {
        if (!cancelled) setFeatures(f);
      })
      .catch(() => {
        // fall back to defaults if the API is unreachable
      });
    return () => {
      cancelled = true;
    };
  }, []);

  return <FeaturesContext.Provider value={features}>{children}</FeaturesContext.Provider>;
}

export function useFeatures(): Features {
  return useContext(FeaturesContext);
}
