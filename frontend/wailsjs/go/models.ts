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
	    start: time.Time;
	    end: time.Time;
	    status: string;
	    detail?: string;
	    until: time.Time;
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
	        this.start = this.convertValues(source["start"], time.Time);
	        this.end = this.convertValues(source["end"], time.Time);
	        this.status = source["status"];
	        this.detail = source["detail"];
	        this.until = this.convertValues(source["until"], time.Time);
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
	    start: time.Time;
	    newOnly: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Request(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.kind = source["kind"];
	        this.channel = source["channel"];
	        this.callSign = source["callSign"];
	        this.start = this.convertValues(source["start"], time.Time);
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
	    created: time.Time;
	    start: time.Time;
	    end: time.Time;
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
	        this.created = this.convertValues(source["created"], time.Time);
	        this.start = this.convertValues(source["start"], time.Time);
	        this.end = this.convertValues(source["end"], time.Time);
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
	    start: time.Time;
	    end: time.Time;
	    fetched: time.Time;
	    channels: Channel[];
	    programs: Record<string, Array<Program>>;
	
	    static createFrom(source: any = {}) {
	        return new Guide(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.lineup = source["lineup"];
	        this.start = this.convertValues(source["start"], time.Time);
	        this.end = this.convertValues(source["end"], time.Time);
	        this.fetched = this.convertValues(source["fetched"], time.Time);
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
	    start: time.Time;
	    end: time.Time;
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
	        this.start = this.convertValues(source["start"], time.Time);
	        this.end = this.convertValues(source["end"], time.Time);
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
	    signal?: Record<string, reception.Estimate>;
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
	    generated: time.Time;
	    place: geo.Place;
	    point: geo.Point;
	    radiusKm: number;
	    presets?: reception.Preset[];
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
	        this.generated = this.convertValues(source["generated"], time.Time);
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

export namespace phase {
	
	export class Mark {
	    name: string;
	    ms: number;
	
	    static createFrom(source: any = {}) {
	        return new Mark(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.ms = source["ms"];
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
	
	export class SweepStatus {
	    running: boolean;
	    startedAt?: time.Time;
	    finishedAt?: time.Time;
	    total: number;
	    found: number;
	    done: number;
	    rf?: number;
	    note?: string;
	
	    static createFrom(source: any = {}) {
	        return new SweepStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.running = source["running"];
	        this.startedAt = this.convertValues(source["startedAt"], time.Time);
	        this.finishedAt = this.convertValues(source["finishedAt"], time.Time);
	        this.total = source["total"];
	        this.found = source["found"];
	        this.done = source["done"];
	        this.rf = source["rf"];
	        this.note = source["note"];
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
	export class MuxStation {
	    facilityId: number;
	    callSign: string;
	    virtualChannel: number;
	    service: string;
	    city: string;
	    state: string;
	    distanceKm: number;
	    bearingDeg: number;
	    erpKw: number;
	    haatM: number;
	    atsc3: boolean;
	
	    static createFrom(source: any = {}) {
	        return new MuxStation(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.facilityId = source["facilityId"];
	        this.callSign = source["callSign"];
	        this.virtualChannel = source["virtualChannel"];
	        this.service = source["service"];
	        this.city = source["city"];
	        this.state = source["state"];
	        this.distanceKm = source["distanceKm"];
	        this.bearingDeg = source["bearingDeg"];
	        this.erpKw = source["erpKw"];
	        this.haatM = source["haatM"];
	        this.atsc3 = source["atsc3"];
	    }
	}
	export class MuxSignal {
	    rf: number;
	    frequencyMhz: number;
	    band: string;
	    tuned: boolean;
	    channels: string[];
	    stations: MuxStation[];
	    signal?: signal.Reading;
	    history?: signal.History;
	
	    static createFrom(source: any = {}) {
	        return new MuxSignal(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.rf = source["rf"];
	        this.frequencyMhz = source["frequencyMhz"];
	        this.band = source["band"];
	        this.tuned = source["tuned"];
	        this.channels = source["channels"];
	        this.stations = this.convertValues(source["stations"], MuxStation);
	        this.signal = this.convertValues(source["signal"], signal.Reading);
	        this.history = this.convertValues(source["history"], signal.History);
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
	export class AntennaChannel {
	    number: string;
	    major: number;
	    minor: number;
	    name: string;
	    callSign: string;
	    baseCall: string;
	    guideId?: string;
	    guideCallSign?: string;
	    network: string;
	    logo?: string;
	    rf: number;
	    facilityId: number;
	    transmitter?: string;
	    via?: string;
	    // Go type: lineup
	    atsc3?: any;
	    signal?: signal.Reading;
	    recent?: signal.Period;
	
	    static createFrom(source: any = {}) {
	        return new AntennaChannel(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.number = source["number"];
	        this.major = source["major"];
	        this.minor = source["minor"];
	        this.name = source["name"];
	        this.callSign = source["callSign"];
	        this.baseCall = source["baseCall"];
	        this.guideId = source["guideId"];
	        this.guideCallSign = source["guideCallSign"];
	        this.network = source["network"];
	        this.logo = source["logo"];
	        this.rf = source["rf"];
	        this.facilityId = source["facilityId"];
	        this.transmitter = source["transmitter"];
	        this.via = source["via"];
	        this.atsc3 = this.convertValues(source["atsc3"], null);
	        this.signal = this.convertValues(source["signal"], signal.Reading);
	        this.recent = this.convertValues(source["recent"], signal.Period);
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
	export class TunerInfo {
	    ready: boolean;
	    reason?: string;
	    name?: string;
	    model?: string;
	    tuners: number;
	    inUse: number;
	    standards: string[];
	    atsc3: boolean;
	    scanning: boolean;
	
	    static createFrom(source: any = {}) {
	        return new TunerInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ready = source["ready"];
	        this.reason = source["reason"];
	        this.name = source["name"];
	        this.model = source["model"];
	        this.tuners = source["tuners"];
	        this.inUse = source["inUse"];
	        this.standards = source["standards"];
	        this.atsc3 = source["atsc3"];
	        this.scanning = source["scanning"];
	    }
	}
	export class Antenna {
	    tuner: TunerInfo;
	    channels: AntennaChannel[];
	    muxes: MuxSignal[];
	    sweep: SweepStatus;
	
	    static createFrom(source: any = {}) {
	        return new Antenna(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.tuner = this.convertValues(source["tuner"], TunerInfo);
	        this.channels = this.convertValues(source["channels"], AntennaChannel);
	        this.muxes = this.convertValues(source["muxes"], MuxSignal);
	        this.sweep = this.convertValues(source["sweep"], SweepStatus);
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
	
	export class ChannelSignal {
	    number: string;
	    rf: number;
	    signal?: signal.Reading;
	    recent?: signal.Period;
	
	    static createFrom(source: any = {}) {
	        return new ChannelSignal(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.number = source["number"];
	        this.rf = source["rf"];
	        this.signal = this.convertValues(source["signal"], signal.Reading);
	        this.recent = this.convertValues(source["recent"], signal.Period);
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
	
	
	export class SignalReport {
	    tuner: TunerInfo;
	    channels: ChannelSignal[];
	    muxes: MuxSignal[];
	    sweep: SweepStatus;
	
	    static createFrom(source: any = {}) {
	        return new SignalReport(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.tuner = this.convertValues(source["tuner"], TunerInfo);
	        this.channels = this.convertValues(source["channels"], ChannelSignal);
	        this.muxes = this.convertValues(source["muxes"], MuxSignal);
	        this.sweep = this.convertValues(source["sweep"], SweepStatus);
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
	    antenna?: Antenna;
	
	    static createFrom(source: any = {}) {
	        return new Snapshot(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.report = this.convertValues(source["report"], lineup.Report);
	        this.guide = this.convertValues(source["guide"], guide.Guide);
	        this.custom = this.convertValues(source["custom"], CustomChannel);
	        this.antenna = this.convertValues(source["antenna"], Antenna);
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

export namespace signal {
	
	export class Period {
	    from: time.Time;
	    to: time.Time;
	    source?: string;
	    samples: number;
	    lockedPct: number;
	    strengthPct?: Range;
	    qualityPct?: Range;
	    symbolPct?: Range;
	
	    static createFrom(source: any = {}) {
	        return new Period(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.from = this.convertValues(source["from"], time.Time);
	        this.to = this.convertValues(source["to"], time.Time);
	        this.source = source["source"];
	        this.samples = source["samples"];
	        this.lockedPct = source["lockedPct"];
	        this.strengthPct = this.convertValues(source["strengthPct"], Range);
	        this.qualityPct = this.convertValues(source["qualityPct"], Range);
	        this.symbolPct = this.convertValues(source["symbolPct"], Range);
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
	export class Range {
	    min: number;
	    avg: number;
	    max: number;
	
	    static createFrom(source: any = {}) {
	        return new Range(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.min = source["min"];
	        this.avg = source["avg"];
	        this.max = source["max"];
	    }
	}
	export class History {
	    from: time.Time;
	    to: time.Time;
	    source?: string;
	    samples: number;
	    lockedPct: number;
	    strengthPct?: Range;
	    qualityPct?: Range;
	    symbolPct?: Range;
	    windows: Period[];
	
	    static createFrom(source: any = {}) {
	        return new History(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.from = this.convertValues(source["from"], time.Time);
	        this.to = this.convertValues(source["to"], time.Time);
	        this.source = source["source"];
	        this.samples = source["samples"];
	        this.lockedPct = source["lockedPct"];
	        this.strengthPct = this.convertValues(source["strengthPct"], Range);
	        this.qualityPct = this.convertValues(source["qualityPct"], Range);
	        this.symbolPct = this.convertValues(source["symbolPct"], Range);
	        this.windows = this.convertValues(source["windows"], Period);
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
	
	
	export class Reading {
	    at: time.Time;
	    source: string;
	    lock: boolean;
	    strengthPct?: number;
	    qualityPct?: number;
	    symbolPct?: number;
	    errorsPerSec?: number;
	
	    static createFrom(source: any = {}) {
	        return new Reading(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.at = this.convertValues(source["at"], time.Time);
	        this.source = source["source"];
	        this.lock = source["lock"];
	        this.strengthPct = source["strengthPct"];
	        this.qualityPct = source["qualityPct"];
	        this.symbolPct = source["symbolPct"];
	        this.errorsPerSec = source["errorsPerSec"];
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
	    timing?: phase.Mark[];
	
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
	        this.timing = this.convertValues(source["timing"], phase.Mark);
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

export namespace time {
	
	export class Time {
	
	
	    static createFrom(source: any = {}) {
	        return new Time(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	
	    }
	}

}

export namespace tuner {
	
	export class Device {
	    id: string;
	    name: string;
	    kind: string;
	    model?: string;
	    tuners?: number;
	    detail?: string;
	    standards?: string[];
	    atsc3: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Device(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.kind = source["kind"];
	        this.model = source["model"];
	        this.tuners = source["tuners"];
	        this.detail = source["detail"];
	        this.standards = source["standards"];
	        this.atsc3 = source["atsc3"];
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
	    onset: time.Time;
	    ends: time.Time;
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
	        this.onset = this.convertValues(source["onset"], time.Time);
	        this.ends = this.convertValues(source["ends"], time.Time);
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
	    start: time.Time;
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
	        this.start = this.convertValues(source["start"], time.Time);
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
	    observed: time.Time;
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
	        this.observed = this.convertValues(source["observed"], time.Time);
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
	    start: time.Time;
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
	        this.start = this.convertValues(source["start"], time.Time);
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
	    rise: time.Time;
	    set: time.Time;
	
	    static createFrom(source: any = {}) {
	        return new Sun(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.rise = this.convertValues(source["rise"], time.Time);
	        this.set = this.convertValues(source["set"], time.Time);
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
	    updated: time.Time;
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
	        this.updated = this.convertValues(source["updated"], time.Time);
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

