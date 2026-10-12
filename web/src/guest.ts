import type { MetricHistory } from './api'
export interface GuestResource {id:string;name:string}
export interface GuestSettings {revision:number;enabled:boolean;title:string;showStatus:boolean;metrics:string[];historyHours:number;nodes:GuestResource[];probes:GuestResource[]}
export interface GuestOptions {settings:GuestSettings;metrics:Array<{key:string;unit:string;history:boolean}>;historyHours:number[]}
export interface GuestDisplay {title:string;generatedAt:string;refreshSeconds:number;nodes:Array<{id:string;name:string;status?:string;metrics?:Record<string,{status:string;unit:string;value?:number;sampledAt:string}>;history?:Omit<MetricHistory,'nodeId'>}>;probes:Array<{id:string;name:string;status:string;checkedAt?:string;latencyMs?:number}>}
