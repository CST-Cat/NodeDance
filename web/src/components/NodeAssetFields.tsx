import { t } from "../i18n";
import type { BodySchema } from "../operations";
import { derived } from "../state";

import { Fragment } from "react";
import { __classes, __each, __input, useController } from "../state";
import "./NodeAssetFields.css";
import UiSelect from "./UiSelect";
import { SettingsRows } from "./SettingsGroup";
import { Input } from "./arc/input/input";
type Props = {
  values: Record<string, unknown>;
  schema?: BodySchema;
  disabled?: boolean;
  showTitle?: boolean;
  showHints?: boolean;
  grouped?: boolean;
} & { children?: import("react").ReactNode; [key: string]: any };
export default function NodeAssetFields(input: Props) {
  return useController(
    (__props: Props, __emit: any, __dom: any) => {
      const props = __props;
      const unitLabels: Record<string, string> = {
        year: "asset.unit.year",
        month: "asset.unit.month",
        hour: "asset.unit.hour",
        minute: "asset.unit.minute",
        day: "asset.unit.day",
      };
      const cycle = derived({
        get: () => String(props.values.billingCycleUnit || "day"),
        set: (value) => {
          props.values.billingCycleUnit = value;
        },
      });
      const units = derived(() =>
        (props.schema?.properties?.billingCycleUnit?.enum ?? []).filter(
          (value) => value !== "",
        ),
      );
      const currencies = derived(() => [
        ...(props.schema?.properties?.currency?.enum ?? []),
      ]);
      const purchase = derived({
        get: () =>
          props.values.purchasedAt
            ? String(props.values.purchasedAt).slice(0, 10)
            : "",
        set: (value) => {
          props.values.purchasedAt = value ? value + "T00:00:00Z" : null;
        },
      });
      const expiry = derived({
        get: () => {
          if (!props.values.expiresAt) return "";
          const date = new Date(String(props.values.expiresAt));
          return Number.isNaN(date.getTime())
            ? ""
            : new Date(date.getTime() - date.getTimezoneOffset() * 60000)
                .toISOString()
                .slice(0, 16);
        },
        set: (value) => {
          props.values.expiresAt = value ? new Date(value).toISOString() : null;
        },
      });
      const traffic = derived({
        get: () => Number(props.values.trafficLimitBytes ?? 0) / 1000000000,
        set: (value) => {
          props.values.trafficLimitBytes = Math.round(
            Number(value) * 1000000000,
          );
        },
      });
      return () => {
        const Rows = __props.grouped ? SettingsRows : Fragment;
        return (
          <>
            <fieldset
              disabled={__props.disabled as any}
              aria-label={
                __props.showTitle === false
                  ? t("asset.title")
                  : (undefined as any)
              }
              data-testid="node-asset-fields"
              className={__classes("asset-fields")}
              data-v-f325f884=""
            >
              {__props.showTitle !== false ? (
                <legend data-v-f325f884="">{t("asset.title")}</legend>
              ) : null}
              <div className={__classes("asset-grid")} data-v-f325f884="">
                <Rows>
                  <label data-v-f325f884="">
                    <span data-v-f325f884="">{t("asset.monthlyTraffic")}</span>
                    <Input
                      type="number"
                      min="0"
                      step="any"
                      data-testid="asset-traffic"
                      {...__input(
                        traffic.value,
                        (__nextValue: any) => {
                          traffic.value = __nextValue;
                        },
                        "number",
                        undefined,
                        [],
                      )}
                      data-v-f325f884=""
                    />
                    {__props.showHints !== false ? (
                      <small data-v-f325f884="">{t("asset.trafficHint")}</small>
                    ) : null}
                  </label>
                  <label className={__classes("asset-date")} data-v-f325f884="">
                    <span data-v-f325f884="">{t("asset.purchase")}</span>
                    <Input
                      type="date"
                      data-testid="asset-purchase"
                      {...__input(
                        purchase.value,
                        (__nextValue: any) => {
                          purchase.value = __nextValue;
                        },
                        "date",
                        undefined,
                        [],
                      )}
                      data-v-f325f884=""
                    />
                    {__props.showHints !== false ? (
                      <small data-v-f325f884="">
                        {t("asset.purchaseHint")}
                      </small>
                    ) : null}
                  </label>
                  <label className={__classes("asset-date")} data-v-f325f884="">
                    <span data-v-f325f884="">{t("asset.expiry")}</span>
                    <Input
                      type="datetime-local"
                      data-testid="asset-expiry"
                      {...__input(
                        expiry.value,
                        (__nextValue: any) => {
                          expiry.value = __nextValue;
                        },
                        "datetime-local",
                        undefined,
                        [],
                      )}
                      data-v-f325f884=""
                    />
                    {__props.showHints !== false ? (
                      <small data-v-f325f884="">{t("asset.expiryHint")}</small>
                    ) : null}
                  </label>
                  <label data-v-f325f884="">
                    <span data-v-f325f884="">{t("asset.price")}</span>
                    <Input
                      type="number"
                      min="0"
                      step="any"
                      data-testid="asset-price"
                      {...__input(
                        __props.values.price,
                        (__nextValue: any) => {
                          __props.values.price = __nextValue;
                        },
                        "number",
                        undefined,
                        ["number"],
                      )}
                      data-v-f325f884=""
                    />
                  </label>
                  <label data-v-f325f884="">
                    <span data-v-f325f884="">{t("host.field.currency")}</span>
                    <UiSelect
                      data-testid="asset-currency"
                      modelValue={__props.values.currency}
                      onUpdateModelValue={(__nextValue: any) => {
                        __props.values.currency = __nextValue;
                      }}
                      modelModifiers={{}}
                      data-v-f325f884=""
                    >
                      <Fragment>
                        {__each(currencies.value, (currency) => (
                          <option
                            key={String(currency) as any}
                            value={currency as any}
                            data-v-f325f884=""
                          >
                            {String(currency)}
                          </option>
                        ))}
                      </Fragment>
                    </UiSelect>
                  </label>
                  <label data-v-f325f884="">
                    <span data-v-f325f884="">{t("asset.cycle")}</span>
                    <UiSelect
                      data-testid="asset-cycle"
                      modelValue={cycle.value}
                      onUpdateModelValue={(__nextValue: any) => {
                        cycle.value = __nextValue;
                      }}
                      modelModifiers={{}}
                      data-v-f325f884=""
                    >
                      <Fragment>
                        {__each(units.value, (unit) => (
                          <option
                            key={String(unit) as any}
                            value={unit as any}
                            data-v-f325f884=""
                          >
                            {t(unitLabels[String(unit)] ?? String(unit))}
                          </option>
                        ))}
                      </Fragment>
                    </UiSelect>
                  </label>
                  {cycle.value === "day" ? (
                    <label data-v-f325f884="">
                      <span data-v-f325f884="">
                        {t("host.field.billingCycleDays")}
                      </span>
                      <Input
                        type="number"
                        min="1"
                        max="3660"
                        {...__input(
                          __props.values.billingCycleDays,
                          (__nextValue: any) => {
                            __props.values.billingCycleDays = __nextValue;
                          },
                          "number",
                          undefined,
                          ["number"],
                        )}
                        data-v-f325f884=""
                      />
                    </label>
                  ) : null}
                </Rows>{" "}
              </div>
            </fieldset>
          </>
        );
      };
    },
    input,
    ["values", "schema", "disabled", "showTitle", "showHints", "grouped"],
    [],
    {},
    true,
  );
}
