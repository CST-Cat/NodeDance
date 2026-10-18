export function formatBytes(value: number | null | undefined): string {
        if (value == null || !Number.isFinite(value) || value < 0) return "—";
        if (value < 1024) return `${Math.round(value)} B`;
        const units = ["KiB", "MiB", "GiB", "TiB", "PiB"];
        let amount = value;
        let unit = "B";
        for (const next of units) {
          amount /= 1024;
          unit = next;
          if (amount < 1024) break;
        }
        return `${amount.toFixed(1)} ${unit}`;
      }
