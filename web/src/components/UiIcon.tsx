import { useController } from "../state";
type Props = {
  name: string;
} & { children?: import("react").ReactNode; [key: string]: any };
export default function UiIcon(input: Props) {
  return useController(
    (__props: Props, __emit: any, __dom: any) => {
      __props;
      const paths: Record<string, string> = {
        cog: "M9 3h6l.6 2.6 2 1.2 2.5-.8 1.8 3.1-1.9 1.8v2.2l1.9 1.8-1.8 3.1-2.5-.8-2 1.2L15 21H9l-.6-2.6-2-1.2-2.5.8-1.8-3.1L4 13.1v-2.2L2.1 9.1 3.9 6l2.5.8 2-1.2z M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8",
        edit: "M4 16v4h4L20 8l-4-4z M14 6l4 4 M12 20h8",
        check: "M5 12l4 4L19 6",
        copy: "M8 8h12v12H8z M4 16H3V3h13v1",
        eye: "M2 12s4-7 10-7 10 7 10 7-4 7-10 7S2 12 2 12z M12 9a3 3 0 1 0 0 6 3 3 0 0 0 0-6",
        eyeOff:
          "M3 3l18 18 M9 5.4A11 11 0 0 1 12 5c6 0 10 7 10 7a20 20 0 0 1-4 4 M14 18.8a10 10 0 0 1-2 .2C6 19 2 12 2 12a21 21 0 0 1 4-4 M10 10a3 3 0 0 0 4 4",
        back: "M20 12H4 M10 6l-6 6 6 6",
        history: "M3 12a9 9 0 1 0 3-6 M3 3v6h6 M12 7v5l3 2",
        folder: "M3 6h7l2 2h9v12H3z M3 6V4h7l2 2",
        file: "M5 3h9l5 5v13H5z M14 3v6h5 M8 13h8 M8 17h6",
        refresh:
          "M20 7v5h-5 M4 17v-5h5 M6 7a7 7 0 0 1 12-1l2 3 M4 15l2 3a7 7 0 0 0 12-1",
        terminal: "M4 4h16v16H4z M7 8l3 3-3 3 M12 15h5",
        host: "M3 4h18v12H3z M12 16v4 M8 20h8 M6 10h3l2-3 3 6 2-3h2",
        subscriptions:
          "M5 17a2 2 0 1 0 0 4 2 2 0 0 0 0-4 M4 10a10 10 0 0 1 10 10 M4 3a17 17 0 0 1 17 17",
        operations:
          "M3 3h6v6H3z M15 9h6v6h-6z M3 15h6v6H3z M9 6h3v12H9 M12 12h3",
        nodes: "M4 4h16v6H4z M4 14h16v6H4z M7 7h.01 M7 17h.01 M11 7h6 M11 17h6",
        guest:
          "M3 5h18v14H3z M3 8h18 M6 6.5h.01 M8 6.5h.01 M6 13.5s2-3.5 6-3.5 6 3.5 6 3.5-2 3.5-6 3.5-6-3.5-6-3.5z M12 12a1.5 1.5 0 1 0 0 3 1.5 1.5 0 0 0 0-3",
        add: "M12 5v14 M5 12h14",
        discovery: "M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18 M16 8l-3 5-5 3 3-5z",
        alerts: "M18 8a6 6 0 0 0-12 0c0 7-3 7-3 9h18c0-2-3-2-3-9 M10 21h4",
        settings:
          "M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8 M12 2v3 M12 19v3 M2 12h3 M19 12h3 M5 5l2 2 M17 17l2 2 M5 19l2-2 M17 7l2-2",
        logout: "M9 4H4v16h5 M9 12h12 M17 8l4 4-4 4",
        menu: "M4 6h16 M4 12h16 M4 18h16",
        close: "M6 6l12 12 M6 18L18 6",
      };
      return () => (
        <>
          <svg
            viewBox="0 0 24 24"
            width="20"
            height="20"
            fill="none"
            stroke="currentColor"
            stroke-width="1.5"
            stroke-linecap="round"
            stroke-linejoin="round"
            aria-hidden="true"
          >
            <path d={paths[__props.name] || (paths.nodes as any)}></path>
          </svg>
        </>
      );
    },
    input,
    ["name"],
    [],
    {},
    true,
  );
}
