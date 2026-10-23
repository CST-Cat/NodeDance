import { useEffect, useState } from "react";
import { operationRequest, type NodePosition, type CityLocation, type NodeLocation } from "../api";
import { locale, t } from "../i18n";
import { Input, Select, Alert } from "./ManagementUI";
import CountryLogo from "./CountryLogo";

export default function LocationPicker({ value, countryCode, countries, schema, resolved, onChange }: {
 resolved?: NodeLocation; value: NodePosition | null; countryCode: string; countries: string[]; schema: Record<string, any>;
 onChange: (value: NodePosition | null) => void;
}) {
 const [mode,setMode]=useState(value?"city":"auto"),[query,setQuery]=useState("");
 const [cities,setCities]=useState<CityLocation[]>([]),[error,setError]=useState("");
 useEffect(()=>{setMode(current=>!value?"auto":current==="auto"?"city":current)},[value]);
 useEffect(()=>{
  if(mode!=="city")return;
  const controller=new AbortController();
  const timer=setTimeout(()=>{void operationRequest<{cities:CityLocation[]}>("locations.search",{query:{q:query,countryCode}},{signal:controller.signal}).then(result=>{if(!controller.signal.aborted){setCities(result.cities);setError("")}}).catch(reason=>{if(!controller.signal.aborted){setCities([]);setError(reason instanceof Error?reason.message:String(reason))}})},250);
  return()=>{clearTimeout(timer);controller.abort()};
 },[mode,query,countryCode]);
 const countryName=(code:string)=>code?(new Intl.DisplayNames([locale.value],{type:"region"}).of(code)??code):"";
 const caption=(point:NodePosition)=>[point.city,point.region,countryName(point.countryCode)].filter(Boolean).join(" · ");
 const update=(patch:Partial<NodePosition>)=>onChange({...value??{countryCode,city:"",region:"",latitude:null,longitude:null},...patch});
 return <div className="location-picker preference-wide">
  <Select label={t("location.mode")} description={mode === "auto" ? (resolved?.source === "manual" ? t("location.afterSave") : resolved?.position ? caption(resolved.position) : t("location.unavailable")) : undefined} value={mode} onValueChange={next=>{
   setMode(next);
   if(next==="auto")onChange(null);
   if(next==="coordinates"&&!value){if(resolved?.position)onChange({...resolved.position});else update({});}
  }} options={[{value:"auto",label:t("location.auto")},{value:"city",label:t("location.selectCity")},{value:"coordinates",label:t("location.coordinates")}]} />
  {mode==="city"?<>
   <Input label={t("location.search")} value={query} onChange={event=>setQuery(event.target.value)} placeholder={t("location.searchHint")} />
   <Select label={t("location.city")} required value={value?"selected":""} placeholder={t("location.choose")} onValueChange={id=>{
    const city=cities.find(city=>city.id===id);if(city)onChange({countryCode:city.countryCode,city:city.city,region:city.region,latitude:city.latitude,longitude:city.longitude});
   }} options={[...(!value?[{value:"",label:t("location.choose"),disabled:true}]:[]),...(value?[{value:"selected",label:caption(value)}]:[]),...cities.filter(city=>!value||city.latitude!==value.latitude||city.longitude!==value.longitude).map(city=>({value:city.id,label:caption(city)}))]} />
   {error?<Alert tone="warning" title={t("location.searchError")}>{error}</Alert>:null}
  </>:null}
  {mode==="coordinates"?<>
   <Select label={t("preferences.country")} required value={value?.countryCode??""} placeholder={t("location.chooseCountry")} onValueChange={code=>update({countryCode:code})} options={[...(!value?.countryCode?[{value:"",label:t("location.chooseCountry"),disabled:true}]:[]),...countries.filter(Boolean).map(code=>({value:code,label:countryName(code),icon:<CountryLogo country={{status:"known",code}}/>})).sort((a,b)=>a.label.localeCompare(b.label,locale.value))]}/>
   <Input label={t("location.city")} required value={value?.city??""} onChange={event=>update({city:event.target.value})}/>
   <Input label={t("location.region")} value={value?.region??""} onChange={event=>update({region:event.target.value})}/>
   {(["latitude","longitude"] as const).map(key=><Input key={key} label={t(`location.${key}`)} type="number" inputMode="decimal" required step="any" min={schema[key]?.minimum} max={schema[key]?.maximum} value={value?.[key]??""} onChange={event=>update({[key]:event.target.value===""?null:Number(event.target.value)})}/>)}
  </>:null}
 </div>;
}
