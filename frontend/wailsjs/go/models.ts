export namespace atsc3 {
	
	export class Service {
	    display: string;
	    major: number;
	    minor: number;
	    network: string;
	    name: string;
	    atsc1Call: string;
	    atsc1Display: string;
	
	    static createFrom(source: any = {}) {
	        return new Service(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.display = source["display"];
	        this.major = source["major"];
	        this.minor = source["minor"];
	        this.network = source["network"];
	        this.name = source["name"];
	        this.atsc1Call = source["atsc1Call"];
	        this.atsc1Display = source["atsc1Display"];
	    }
	}
	export class Host {
	    market: string;
	    callSign: string;
	    facilityId: number;
	    rf: string;
	    launched: string;
	    simulcast: string;
	    services: Service[];
	
	    static createFrom(source: any = {}) {
	        return new Host(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.market = source["market"];
	        this.callSign = source["callSign"];
	        this.facilityId = source["facilityId"];
	        this.rf = source["rf"];
	        this.launched = source["launched"];
	        this.simulcast = source["simulcast"];
	        this.services = this.convertValues(source["services"], Service);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace dvr {
	
	export class Item {
	    id: string;
	    key: string;
	    ruleId?: string;
	    title: string;
	    subtitle?: string;
	    description?: string;
	    image?: string;
	    channel: string;
	    callSign?: string;
	    // Go type: time
	    start: any;
	    // Go type: time
	    end: any;
	    status: string;
	    detail?: string;
	    sizeBytes?: number;
	    duration?: number;
	    position?: number;
	    watched?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Item(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.key = source["key"];
	        this.ruleId = source["ruleId"];
	        this.title = source["title"];
	        this.subtitle = source["subtitle"];
	        this.description = source["description"];
	        this.image = source["image"];
	        this.channel = source["channel"];
	        this.callSign = source["callSign"];
	        this.start = this.convertValues(source["start"], null);
	        this.end = this.convertValues(source["end"], null);
	        this.status = source["status"];
	        this.detail = source["detail"];
	        this.sizeBytes = source["sizeBytes"];
	        this.duration = source["duration"];
	        this.position = source["position"];
	        this.watched = source["watched"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Prefs {
	    deleteWatchedAfterDays: number;
	
	    static createFrom(source: any = {}) {
	        return new Prefs(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.deleteWatchedAfterDays = source["deleteWatchedAfterDays"];
	    }
	}
	export class Request {
	    kind: string;
	    channel: string;
	    callSign: string;
	    // Go type: time
	    start: any;
	    newOnly: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Request(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.kind = source["kind"];
	        this.channel = source["channel"];
	        this.callSign = source["callSign"];
	        this.start = this.convertValues(source["start"], null);
	        this.newOnly = source["newOnly"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Rule {
	    id: string;
	    kind: string;
	    title: string;
	    channel: string;
	    callSign: string;
	    guideId: string;
	    seriesId?: string;
	    newOnly?: boolean;
	    image?: string;
	    // Go type: time
	    created: any;
	    // Go type: time
	    start: any;
	    // Go type: time
	    end: any;
	    subtitle?: string;
	    description?: string;
	    programId?: string;
	    sports?: boolean;
	    skip?: string[];
	    keep?: number;
	
	    static createFrom(source: any = {}) {
	        return new Rule(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.kind = source["kind"];
	        this.title = source["title"];
	        this.channel = source["channel"];
	        this.callSign = source["callSign"];
	        this.guideId = source["guideId"];
	        this.seriesId = source["seriesId"];
	        this.newOnly = source["newOnly"];
	        this.image = source["image"];
	        this.created = this.convertValues(source["created"], null);
	        this.start = this.convertValues(source["start"], null);
	        this.end = this.convertValues(source["end"], null);
	        this.subtitle = source["subtitle"];
	        this.description = source["description"];
	        this.programId = source["programId"];
	        this.sports = source["sports"];
	        this.skip = source["skip"];
	        this.keep = source["keep"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class RuleUpdate {
	    keep: number;
	    newOnly: boolean;
	
	    static createFrom(source: any = {}) {
	        return new RuleUpdate(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.keep = source["keep"];
	        this.newOnly = source["newOnly"];
	    }
	}
	export class State {
	    available: boolean;
	    reason?: string;
	    rules: Rule[];
	    upcoming: Item[];
	    recorded: Item[];
	    prefs: Prefs;
	
	    static createFrom(source: any = {}) {
	        return new State(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.available = source["available"];
	        this.reason = source["reason"];
	        this.rules = this.convertValues(source["rules"], Rule);
	        this.upcoming = this.convertValues(source["upcoming"], Item);
	        this.recorded = this.convertValues(source["recorded"], Item);
	        this.prefs = this.convertValues(source["prefs"], Prefs);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace geo {
	
	export class Point {
	    lat: number;
	    lon: number;
	
	    static createFrom(source: any = {}) {
	        return new Point(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.lat = source["lat"];
	        this.lon = source["lon"];
	    }
	}
	export class Place {
	    zip: string;
	    name: string;
	    state: string;
	    point: Point;
	
	    static createFrom(source: any = {}) {
	        return new Place(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.zip = source["zip"];
	        this.name = source["name"];
	        this.state = source["state"];
	        this.point = this.convertValues(source["point"], Point);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace guide {
	
	export class Channel {
	    id: string;
	    number: string;
	    callSign: string;
	    network: string;
	    logo: string;
	
	    static createFrom(source: any = {}) {
	        return new Channel(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.number = source["number"];
	        this.callSign = source["callSign"];
	        this.network = source["network"];
	        this.logo = source["logo"];
	    }
	}
	export class Guide {
	    lineup: string;
	    // Go type: time
	    start: any;
	    // Go type: time
	    end: any;
	    // Go type: time
	    fetched: any;
	    channels: Channel[];
	    programs: Record<string, Array<Program>>;
	
	    static createFrom(source: any = {}) {
	        return new Guide(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.lineup = source["lineup"];
	        this.start = this.convertValues(source["start"], null);
	        this.end = this.convertValues(source["end"], null);
	        this.fetched = this.convertValues(source["fetched"], null);
	        this.channels = this.convertValues(source["channels"], Channel);
	        this.programs = this.convertValues(source["programs"], Array<Program>, true);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Program {
	    // Go type: time
	    start: any;
	    // Go type: time
	    end: any;
	    title: string;
	    episodeTitle?: string;
	    description?: string;
	    season?: string;
	    episode?: string;
	    rating?: string;
	    year?: string;
	    flags?: string[];
	    tags?: string[];
	    genres?: string[];
	    image?: string;
	    programId?: string;
	    seriesId?: string;
	    audioLang?: string;
	
	    static createFrom(source: any = {}) {
	        return new Program(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.start = this.convertValues(source["start"], null);
	        this.end = this.convertValues(source["end"], null);
	        this.title = source["title"];
	        this.episodeTitle = source["episodeTitle"];
	        this.description = source["description"];
	        this.season = source["season"];
	        this.episode = source["episode"];
	        this.rating = source["rating"];
	        this.year = source["year"];
	        this.flags = source["flags"];
	        this.tags = source["tags"];
	        this.genres = source["genres"];
	        this.image = source["image"];
	        this.programId = source["programId"];
	        this.seriesId = source["seriesId"];
	        this.audioLang = source["audioLang"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace lineup {
	
	export class Carriage {
	    hostCall: string;
	    facilityId: number;
	    rf: string;
	    display: string;
	    tier: Record<string, string>;
	
	    static createFrom(source: any = {}) {
	        return new Carriage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hostCall = source["hostCall"];
	        this.facilityId = source["facilityId"];
	        this.rf = source["rf"];
	        this.display = source["display"];
	        this.tier = source["tier"];
	    }
	}
	export class Channel {
	    number: string;
	    major: number;
	    minor: number;
	    guideId?: string;
	    callSign: string;
	    baseCall: string;
	    network: string;
	    logo?: string;
	    facilityId: number;
	    via?: string;
	    tier: Record<string, string>;
	    atsc3?: Carriage;
	    atsc3Only?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Channel(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.number = source["number"];
	        this.major = source["major"];
	        this.minor = source["minor"];
	        this.guideId = source["guideId"];
	        this.callSign = source["callSign"];
	        this.baseCall = source["baseCall"];
	        this.network = source["network"];
	        this.logo = source["logo"];
	        this.facilityId = source["facilityId"];
	        this.via = source["via"];
	        this.tier = source["tier"];
	        this.atsc3 = this.convertValues(source["atsc3"], Carriage);
	        this.atsc3Only = source["atsc3Only"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Source {
	    name: string;
	    url: string;
	    use: string;
	
	    static createFrom(source: any = {}) {
	        return new Source(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.url = source["url"];
	        this.use = source["use"];
	    }
	}
	export class Station {
	    callSign: string;
	    baseCall: string;
	    service: string;
	    rfChannel: number;
	    virtualChannel: number;
	    status: string;
	    city: string;
	    state: string;
	    facilityId: number;
	    licensee: string;
	    erpKw: number;
	    haatM: number;
	    rcamslM: number;
	    rcaglM: number;
	    directional: boolean;
	    point: geo.Point;
	    distanceKm: number;
	    bearingDeg: number;
	    band: string;
	    signal: Record<string, reception.Estimate>;
	    atsc3: boolean;
	    carries: string[];
	
	    static createFrom(source: any = {}) {
	        return new Station(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.callSign = source["callSign"];
	        this.baseCall = source["baseCall"];
	        this.service = source["service"];
	        this.rfChannel = source["rfChannel"];
	        this.virtualChannel = source["virtualChannel"];
	        this.status = source["status"];
	        this.city = source["city"];
	        this.state = source["state"];
	        this.facilityId = source["facilityId"];
	        this.licensee = source["licensee"];
	        this.erpKw = source["erpKw"];
	        this.haatM = source["haatM"];
	        this.rcamslM = source["rcamslM"];
	        this.rcaglM = source["rcaglM"];
	        this.directional = source["directional"];
	        this.point = this.convertValues(source["point"], geo.Point);
	        this.distanceKm = source["distanceKm"];
	        this.bearingDeg = source["bearingDeg"];
	        this.band = source["band"];
	        this.signal = this.convertValues(source["signal"], reception.Estimate, true);
	        this.atsc3 = source["atsc3"];
	        this.carries = source["carries"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Report {
	    // Go type: time
	    generated: any;
	    place: geo.Place;
	    point: geo.Point;
	    radiusKm: number;
	    presets: reception.Preset[];
	    stations: Station[];
	    channels: Channel[];
	    atsc3: atsc3.Host[];
	    sources: Source[];
	    warnings: string[];
	
	    static createFrom(source: any = {}) {
	        return new Report(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.generated = this.convertValues(source["generated"], null);
	        this.place = this.convertValues(source["place"], geo.Place);
	        this.point = this.convertValues(source["point"], geo.Point);
	        this.radiusKm = source["radiusKm"];
	        this.presets = this.convertValues(source["presets"], reception.Preset);
	        this.stations = this.convertValues(source["stations"], Station);
	        this.channels = this.convertValues(source["channels"], Channel);
	        this.atsc3 = this.convertValues(source["atsc3"], atsc3.Host);
	        this.sources = this.convertValues(source["sources"], Source);
	        this.warnings = source["warnings"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	

}

export namespace main {
	
	export class Boot {
	    settings: store.Settings;
	    presets: reception.Preset[];
	    info: service.Info;
	    config: service.Config;
	    error?: string;
	    serverUrl?: string;
	    view: string;
	    lite?: string;
	    autoScale: number;
	    nativeZoom?: boolean;
	    version: string;
	
	    static createFrom(source: any = {}) {
	        return new Boot(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.settings = this.convertValues(source["settings"], store.Settings);
	        this.presets = this.convertValues(source["presets"], reception.Preset);
	        this.info = this.convertValues(source["info"], service.Info);
	        this.config = this.convertValues(source["config"], service.Config);
	        this.error = source["error"];
	        this.serverUrl = source["serverUrl"];
	        this.view = source["view"];
	        this.lite = source["lite"];
	        this.autoScale = source["autoScale"];
	        this.nativeZoom = source["nativeZoom"];
	        this.version = source["version"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace reception {
	
	export class Estimate {
	    preset: string;
	    powerDbm: number;
	    noiseMarginDb: number;
	    fieldDbuVm: number;
	    pathLossDb: number;
	    diffractionDb: number;
	    lineOfSight: boolean;
	    tier: string;
	
	    static createFrom(source: any = {}) {
	        return new Estimate(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.preset = source["preset"];
	        this.powerDbm = source["powerDbm"];
	        this.noiseMarginDb = source["noiseMarginDb"];
	        this.fieldDbuVm = source["fieldDbuVm"];
	        this.pathLossDb = source["pathLossDb"];
	        this.diffractionDb = source["diffractionDb"];
	        this.lineOfSight = source["lineOfSight"];
	        this.tier = source["tier"];
	    }
	}
	export class Preset {
	    name: string;
	    label: string;
	    heightM: number;
	    gainDbi: Record<string, number>;
	    lossDb: number;
	
	    static createFrom(source: any = {}) {
	        return new Preset(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.label = source["label"];
	        this.heightM = source["heightM"];
	        this.gainDbi = source["gainDbi"];
	        this.lossDb = source["lossDb"];
	    }
	}

}

export namespace service {
	
	export class Config {
	    zip: string;
	    lat: number;
	    lon: number;
	    radiusKm: number;
	    guideHours: number;
	
	    static createFrom(source: any = {}) {
	        return new Config(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.zip = source["zip"];
	        this.lat = source["lat"];
	        this.lon = source["lon"];
	        this.radiusKm = source["radiusKm"];
	        this.guideHours = source["guideHours"];
	    }
	}
	export class CustomChannel {
	    number: string;
	    name: string;
	    kind: string;
	    callSign?: string;
	    category: string;
	    description?: string;
	    logo?: string;
	    programs: guide.Program[];
	
	    static createFrom(source: any = {}) {
	        return new CustomChannel(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.number = source["number"];
	        this.name = source["name"];
	        this.kind = source["kind"];
	        this.callSign = source["callSign"];
	        this.category = source["category"];
	        this.description = source["description"];
	        this.logo = source["logo"];
	        this.programs = this.convertValues(source["programs"], guide.Program);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Info {
	    name: string;
	    mode: string;
	    version: string;
	    tuner: tuner.Device;
	    dvr: boolean;
	    playback?: string;
	    weatherStar?: boolean;
	    antenna: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Info(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.mode = source["mode"];
	        this.version = source["version"];
	        this.tuner = this.convertValues(source["tuner"], tuner.Device);
	        this.dvr = source["dvr"];
	        this.playback = source["playback"];
	        this.weatherStar = source["weatherStar"];
	        this.antenna = source["antenna"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class PathProfile {
	    distanceKm: number;
	    elevations: number[];
	    rxGroundM: number;
	    txHeightM: number;
	    presets: reception.Preset[];
	    signal: Record<string, reception.Estimate>;
	
	    static createFrom(source: any = {}) {
	        return new PathProfile(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.distanceKm = source["distanceKm"];
	        this.elevations = source["elevations"];
	        this.rxGroundM = source["rxGroundM"];
	        this.txHeightM = source["txHeightM"];
	        this.presets = this.convertValues(source["presets"], reception.Preset);
	        this.signal = this.convertValues(source["signal"], reception.Estimate, true);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Snapshot {
	    report?: lineup.Report;
	    guide?: guide.Guide;
	    custom: CustomChannel[];
	
	    static createFrom(source: any = {}) {
	        return new Snapshot(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.report = this.convertValues(source["report"], lineup.Report);
	        this.guide = this.convertValues(source["guide"], guide.Guide);
	        this.custom = this.convertValues(source["custom"], CustomChannel);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace store {
	
	export class Settings {
	    server: string;
	    token: string;
	    clientId: string;
	    antenna: string;
	    showAll: boolean;
	    lastChannel: string;
	    favorites?: string[];
	    hidden?: string[];
	    captions: boolean;
	    audioLang?: string;
	    scale?: number;
	
	    static createFrom(source: any = {}) {
	        return new Settings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.server = source["server"];
	        this.token = source["token"];
	        this.clientId = source["clientId"];
	        this.antenna = source["antenna"];
	        this.showAll = source["showAll"];
	        this.lastChannel = source["lastChannel"];
	        this.favorites = source["favorites"];
	        this.hidden = source["hidden"];
	        this.captions = source["captions"];
	        this.audioLang = source["audioLang"];
	        this.scale = source["scale"];
	    }
	}

}

export namespace stream {
	
	export class Playback {
	    id: string;
	    url: string;
	    path: string;
	    note?: string;
	    offset?: number;
	
	    static createFrom(source: any = {}) {
	        return new Playback(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.url = source["url"];
	        this.path = source["path"];
	        this.note = source["note"];
	        this.offset = source["offset"];
	    }
	}

}

export namespace tuner {
	
	export class Device {
	    id: string;
	    name: string;
	    kind: string;
	    model?: string;
	    baseUrl?: string;
	    tuners?: number;
	    detail?: string;
	
	    static createFrom(source: any = {}) {
	        return new Device(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.kind = source["kind"];
	        this.model = source["model"];
	        this.baseUrl = source["baseUrl"];
	        this.tuners = source["tuners"];
	        this.detail = source["detail"];
	    }
	}

}

export namespace weather {
	
	export class Air {
	    aqi: number;
	    category: string;
	    pm25: number;
	    ozone: number;
	
	    static createFrom(source: any = {}) {
	        return new Air(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.aqi = source["aqi"];
	        this.category = source["category"];
	        this.pm25 = source["pm25"];
	        this.ozone = source["ozone"];
	    }
	}
	export class Alert {
	    event: string;
	    headline: string;
	    severity: string;
	    urgency: string;
	    // Go type: time
	    onset: any;
	    // Go type: time
	    ends: any;
	    description: string;
	    instruction: string;
	
	    static createFrom(source: any = {}) {
	        return new Alert(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.event = source["event"];
	        this.headline = source["headline"];
	        this.severity = source["severity"];
	        this.urgency = source["urgency"];
	        this.onset = this.convertValues(source["onset"], null);
	        this.ends = this.convertValues(source["ends"], null);
	        this.description = source["description"];
	        this.instruction = source["instruction"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Hour {
	    // Go type: time
	    start: any;
	    tempF: number;
	    precip: number;
	    short: string;
	    wind: string;
	    isDay: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Hour(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.start = this.convertValues(source["start"], null);
	        this.tempF = source["tempF"];
	        this.precip = source["precip"];
	        this.short = source["short"];
	        this.wind = source["wind"];
	        this.isDay = source["isDay"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Now {
	    station: string;
	    // Go type: time
	    observed: any;
	    description: string;
	    tempF?: number;
	    feelsLikeF?: number;
	    dewpointF?: number;
	    humidity?: number;
	    windMph?: number;
	    gustMph?: number;
	    windDir: string;
	    pressureInHg?: number;
	    visibilityMi?: number;
	
	    static createFrom(source: any = {}) {
	        return new Now(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.station = source["station"];
	        this.observed = this.convertValues(source["observed"], null);
	        this.description = source["description"];
	        this.tempF = source["tempF"];
	        this.feelsLikeF = source["feelsLikeF"];
	        this.dewpointF = source["dewpointF"];
	        this.humidity = source["humidity"];
	        this.windMph = source["windMph"];
	        this.gustMph = source["gustMph"];
	        this.windDir = source["windDir"];
	        this.pressureInHg = source["pressureInHg"];
	        this.visibilityMi = source["visibilityMi"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Period {
	    name: string;
	    // Go type: time
	    start: any;
	    isDay: boolean;
	    tempF: number;
	    short: string;
	    detailed: string;
	    precip: number;
	    wind: string;
	
	    static createFrom(source: any = {}) {
	        return new Period(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.start = this.convertValues(source["start"], null);
	        this.isDay = source["isDay"];
	        this.tempF = source["tempF"];
	        this.short = source["short"];
	        this.detailed = source["detailed"];
	        this.precip = source["precip"];
	        this.wind = source["wind"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Sun {
	    // Go type: time
	    rise: any;
	    // Go type: time
	    set: any;
	
	    static createFrom(source: any = {}) {
	        return new Sun(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.rise = this.convertValues(source["rise"], null);
	        this.set = this.convertValues(source["set"], null);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Report {
	    // Go type: time
	    updated: any;
	    now?: Now;
	    periods: Period[];
	    hourly: Hour[];
	    alerts: Alert[];
	    air?: Air;
	    sun?: Sun;
	    radar?: string;
	    satellite?: string;
	    errors?: string[];
	
	    static createFrom(source: any = {}) {
	        return new Report(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.updated = this.convertValues(source["updated"], null);
	        this.now = this.convertValues(source["now"], Now);
	        this.periods = this.convertValues(source["periods"], Period);
	        this.hourly = this.convertValues(source["hourly"], Hour);
	        this.alerts = this.convertValues(source["alerts"], Alert);
	        this.air = this.convertValues(source["air"], Air);
	        this.sun = this.convertValues(source["sun"], Sun);
	        this.radar = source["radar"];
	        this.satellite = source["satellite"];
	        this.errors = source["errors"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

