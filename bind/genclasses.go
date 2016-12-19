// Copyright 2016 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package bind

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/mobile/internal/importers/java"
)

type (
	// ClassGen generates Go and C stubs for Java classes so import statements
	// on the form
	//
	//
	// import "Java/classpath/to/Class"
	//
	// will work.
	ClassGen struct {
		*Printer
		imported map[string]struct{}
		// The list of imported Java classes
		classes []*java.Class
		// The list of Go package paths with Java interfaces inside
		jpkgs []string
		// For each Go package path, the list of Java classes.
		typePkgs map[string][]*java.Class
		// For each Go package path, the Java class with static functions
		// or constants.
		clsPkgs map[string]*java.Class
	}
)

func (g *ClassGen) isSupported(t *java.Type) bool {
	switch t.Kind {
	case java.Array:
		// TODO: Support all array types
		return t.Elem.Kind == java.Byte
	default:
		return true
	}
}

func (g *ClassGen) isFuncSupported(f *java.Func) bool {
	for _, a := range f.Params {
		if !g.isSupported(a) {
			return false
		}
	}
	if f.Ret != nil {
		return g.isSupported(f.Ret)
	}
	return true
}

func (g *ClassGen) goType(t *java.Type, local bool) string {
	switch t.Kind {
	case java.Int:
		return "int32"
	case java.Boolean:
		return "bool"
	case java.Short:
		return "int16"
	case java.Char:
		return "uint16"
	case java.Byte:
		return "byte"
	case java.Long:
		return "int64"
	case java.Float:
		return "float32"
	case java.Double:
		return "float64"
	case java.String:
		return "string"
	case java.Array:
		return "[]" + g.goType(t.Elem, local)
	case java.Object:
		if _, exists := g.imported[t.Class]; !exists {
			return "interface{}"
		}
		name := goClsName(t.Class)
		if !local {
			name = "Java." + name
		}
		return name
	default:
		panic("invalid kind")
	}
}

func (g *ClassGen) Init(classes []*java.Class) {
	g.classes = classes
	g.imported = make(map[string]struct{})
	g.typePkgs = make(map[string][]*java.Class)
	g.clsPkgs = make(map[string]*java.Class)
	pkgSet := make(map[string]struct{})
	for _, cls := range classes {
		g.imported[cls.Name] = struct{}{}
		clsPkg := strings.Replace(cls.Name, ".", "/", -1)
		g.clsPkgs[clsPkg] = cls
		typePkg := filepath.Dir(clsPkg)
		g.typePkgs[typePkg] = append(g.typePkgs[typePkg], cls)
		if _, exists := pkgSet[clsPkg]; !exists {
			pkgSet[clsPkg] = struct{}{}
			g.jpkgs = append(g.jpkgs, clsPkg)
		}
		if _, exists := pkgSet[typePkg]; !exists {
			pkgSet[typePkg] = struct{}{}
			g.jpkgs = append(g.jpkgs, typePkg)
		}
	}
}

// Packages return the list of Go packages to be generated.
func (g *ClassGen) Packages() []string {
	return g.jpkgs
}

func (g *ClassGen) GenPackage(idx int) {
	jpkg := g.jpkgs[idx]
	g.Printf("// File is generated by gobind. Do not edit.\n\n")
	g.Printf("package %s\n\n", filepath.Base(jpkg))
	g.Printf("import \"Java\"\n\n")
	g.Printf("const _ = Java.Dummy\n\n")
	for _, cls := range g.typePkgs[jpkg] {
		g.Printf("type %s Java.%s\n", cls.PkgName, goClsName(cls.Name))
	}
	if cls, ok := g.clsPkgs[jpkg]; ok {
		g.Printf("const (\n")
		g.Indent()
		// Constants
		for _, v := range cls.Vars {
			if g.isSupported(v.Type) && v.Constant() {
				g.Printf("%s = %s\n", initialUpper(v.Name), v.Val)
			}
		}
		g.Outdent()
		g.Printf(")\n\n")

		g.Printf("var (\n")
		g.Indent()
		// Functions
		for _, f := range cls.Funcs {
			if !f.Public || !g.isFuncSupported(f) {
				continue
			}
			g.Printf("%s func", f.GoName)
			g.genFuncDecl(false, f)
			g.Printf("\n")
		}
		g.Printf("// Cast takes a proxy for a Java object and converts it to a %s proxy.\n", cls.Name)
		g.Printf("// Cast panics if the argument is not a proxy or if the underlying object does\n")
		g.Printf("// not extend or implement %s.\n", cls.Name)
		g.Printf("Cast func(v interface{}) Java.%s\n", goClsName(cls.Name))
		g.Outdent()
		g.Printf(")\n\n")
	}
}

func (g *ClassGen) GenGo() {
	g.Printf(classesGoHeader)
	for _, cls := range g.classes {
		pkgName := strings.Replace(cls.Name, ".", "/", -1)
		g.Printf("import %q\n", "Java/"+pkgName)
	}
	if len(g.classes) > 0 {
		g.Printf("import \"unsafe\"\n\n")
		g.Printf("import \"reflect\"\n\n")
		g.Printf("import \"fmt\"\n\n")
	}
	g.Printf("type proxy interface { Bind_proxy_refnum__() int32 }\n\n")
	g.Printf("// Suppress unused package error\n\n")
	g.Printf("var _ = _seq.FromRefNum\n")
	g.Printf("const _ = Java.Dummy\n\n")
	g.Printf("//export initClasses\n")
	g.Printf("func initClasses() {\n")
	g.Indent()
	g.Printf("C.init_proxies()\n")
	for _, cls := range g.classes {
		g.Printf("init_%s()\n", cls.JNIName)
	}
	g.Outdent()
	g.Printf("}\n\n")
	for _, cls := range g.classes {
		g.genGo(cls)
	}
}

func (g *ClassGen) GenH() {
	g.Printf(classesHHeader)
	for _, tn := range []string{"jint", "jboolean", "jshort", "jchar", "jbyte", "jlong", "jfloat", "jdouble", "nstring", "nbyteslice"} {
		g.Printf("typedef struct ret_%s {\n", tn)
		g.Printf("	%s res;\n", tn)
		g.Printf("	jint exc;\n")
		g.Printf("} ret_%s;\n", tn)
	}
	g.Printf("\n")
	for _, cls := range g.classes {
		for _, f := range cls.AllMethods {
			if !g.isFuncSupported(f) {
				continue
			}
			g.Printf("extern ")
			g.genCMethodDecl("cproxy", cls.JNIName, f)
			g.Printf(";\n")
			if cls.HasSuper() {
				g.Printf("extern ")
				g.genCMethodDecl("csuper", cls.JNIName, f)
				g.Printf(";\n")
			}
		}
	}
	for _, cls := range g.classes {
		g.genH(cls)
	}
}

func (g *ClassGen) GenC() {
	g.Printf(classesCHeader)
	for _, cls := range g.classes {
		g.genC(cls)
		g.Printf("static jclass class_%s;\n", cls.JNIName)
		for _, f := range cls.AllMethods {
			if g.isFuncSupported(f) {
				g.Printf("static jmethodID m_%s_%s;\n", cls.JNIName, f.JNIName)
			}
		}
	}
	g.Printf("\n")
	g.Printf("void init_proxies() {\n")
	g.Indent()
	g.Printf("JNIEnv *env = go_seq_push_local_frame(%d);\n", len(g.classes))
	g.Printf("jclass clazz;\n")
	for _, cls := range g.classes {
		g.Printf("clazz = (*env)->FindClass(env, %q);\n", strings.Replace(cls.FindName, ".", "/", -1))
		g.Printf("class_%s = (*env)->NewGlobalRef(env, clazz);\n", cls.JNIName)
		for _, f := range cls.AllMethods {
			if g.isFuncSupported(f) {
				g.Printf("m_%s_%s = go_seq_get_method_id(clazz, %q, %q);\n", cls.JNIName, f.JNIName, f.Name, f.Desc)
			}
		}
	}
	g.Printf("go_seq_pop_local_frame(env);\n")
	g.Outdent()
	g.Printf("}\n\n")
	for _, cls := range g.classes {
		for _, f := range cls.AllMethods {
			if !g.isFuncSupported(f) {
				continue
			}
			g.genCMethodDecl("cproxy", cls.JNIName, f)
			g.genCMethodBody(cls, f, false)
			if cls.HasSuper() {
				g.genCMethodDecl("csuper", cls.JNIName, f)
				g.genCMethodBody(cls, f, true)
			}
		}
	}
}

func (g *ClassGen) GenInterfaces() {
	g.Printf(classesPkgHeader)
	for _, cls := range g.classes {
		g.genInterface(cls)
	}
}

func (g *ClassGen) genCMethodBody(cls *java.Class, f *java.Func, virtual bool) {
	g.Printf(" {\n")
	g.Indent()
	// Add 1 for the 'this' argument
	g.Printf("JNIEnv *env = go_seq_push_local_frame(%d);\n", len(f.Params)+1)
	g.Printf("// Must be a Java object\n")
	g.Printf("jobject _this = go_seq_from_refnum(env, this, NULL, NULL);\n")
	for i, a := range f.Params {
		g.genCToJava(fmt.Sprintf("a%d", i), a)
	}
	if f.Ret != nil {
		g.Printf("%s res = ", f.Ret.JNIType())
	}
	g.Printf("(*env)->Call")
	if virtual {
		g.Printf("Nonvirtual")
	}
	if f.Ret != nil {
		g.Printf(f.Ret.JNICallType())
	} else {
		g.Printf("Void")
	}
	g.Printf("Method(env, _this, ")
	if virtual {
		g.Printf("class_%s, ", cls.JNIName)
	}
	g.Printf("m_%s_%s", cls.JNIName, f.JNIName)
	for i := range f.Params {
		g.Printf(", _a%d", i)
	}
	g.Printf(");\n")
	g.Printf("jobject _exc = go_seq_get_exception(env);\n")
	g.Printf("int32_t _exc_ref = go_seq_to_refnum(env, _exc);\n")
	if f.Ret != nil {
		g.genCRetClear("res", f.Ret, "_exc")
		g.genJavaToC("res", f.Ret)
	}
	g.Printf("go_seq_pop_local_frame(env);\n")
	if f.Ret != nil {
		g.Printf("ret_%s __res = {_res, _exc_ref};\n", f.Ret.CType())
		g.Printf("return __res;\n")
	} else {
		g.Printf("return _exc_ref;\n")
	}
	g.Outdent()
	g.Printf("}\n\n")
}

func initialUpper(s string) string {
	if s == "" {
		return ""
	}
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[n:]
}

func (g *ClassGen) genFuncDecl(local bool, f *java.Func) {
	g.Printf("(")
	for i, a := range f.Params {
		if i > 0 {
			g.Printf(", ")
		}
		g.Printf("a%d %s", i, g.goType(a, local))
	}
	g.Printf(")")
	if f.Throws != "" {
		if f.Ret != nil {
			g.Printf(" (%s, error)", g.goType(f.Ret, local))
		} else {
			g.Printf(" error")
		}
	} else if f.Ret != nil {
		g.Printf(" %s", g.goType(f.Ret, local))
	}
}

func (g *ClassGen) genC(cls *java.Class) {
	for _, f := range cls.Funcs {
		if !f.Public || !g.isFuncSupported(f) {
			continue
		}
		g.genCFuncDecl(cls.JNIName, f)
		g.Printf(" {\n")
		g.Indent()
		g.Printf("JNIEnv *env = go_seq_push_local_frame(%d);\n", len(f.Params))
		for i, a := range f.Params {
			g.genCToJava(fmt.Sprintf("a%d", i), a)
		}
		if f.Constructor {
			g.Printf("jobject res = (*env)->NewObject(env")
		} else if f.Ret != nil {
			g.Printf("%s res = (*env)->CallStatic%sMethod(env", f.Ret.JNIType(), f.Ret.JNICallType())
		} else {
			g.Printf("(*env)->CallStaticVoidMethod(env")
		}
		g.Printf(", clazz, m")
		for i := range f.Params {
			g.Printf(", _a%d", i)
		}
		g.Printf(");\n")
		g.Printf("jobject _exc = go_seq_get_exception(env);\n")
		g.Printf("int32_t _exc_ref = go_seq_to_refnum(env, _exc);\n")
		if f.Ret != nil {
			g.genCRetClear("res", f.Ret, "_exc")
			g.genJavaToC("res", f.Ret)
		}
		g.Printf("go_seq_pop_local_frame(env);\n")
		if f.Ret != nil {
			g.Printf("ret_%s __res = {_res, _exc_ref};\n", f.Ret.CType())
			g.Printf("return __res;\n")
		} else {
			g.Printf("return _exc_ref;\n")
		}
		g.Outdent()
		g.Printf("}\n\n")
	}
}

func (g *ClassGen) genH(cls *java.Class) {
	for _, f := range cls.Funcs {
		if !f.Public || !g.isFuncSupported(f) {
			continue
		}
		g.Printf("extern ")
		g.genCFuncDecl(cls.JNIName, f)
		g.Printf(";\n")
	}
}

func (g *ClassGen) genCMethodDecl(prefix, jniName string, f *java.Func) {
	if f.Ret != nil {
		g.Printf("ret_%s", f.Ret.CType())
	} else {
		// Return only the exception, if any
		g.Printf("jint")
	}
	g.Printf(" %s_%s_%s(jint this", prefix, jniName, f.JNIName)
	for i, a := range f.Params {
		g.Printf(", %s a%d", a.CType(), i)
	}
	g.Printf(")")
}

func (g *ClassGen) genCFuncDecl(jniName string, f *java.Func) {
	if f.Ret != nil {
		g.Printf("ret_%s", f.Ret.CType())
	} else {
		// Return only the exception, if any
		g.Printf("jint")
	}
	g.Printf(" cproxy_s_%s_%s(jclass clazz, jmethodID m", jniName, f.JNIName)
	for i, a := range f.Params {
		g.Printf(", %s a%d", a.CType(), i)
	}
	g.Printf(")")
}

func (g *ClassGen) genGo(cls *java.Class) {
	g.Printf("var class_%s C.jclass\n\n", cls.JNIName)
	g.Printf("func init_%s() {\n", cls.JNIName)
	g.Indent()
	g.Printf("cls := C.CString(%q)\n", strings.Replace(cls.FindName, ".", "/", -1))
	g.Printf("clazz := C.go_seq_find_class(cls)\n")
	g.Printf("C.free(unsafe.Pointer(cls))\n")
	g.Printf("if clazz == nil {\n")
	g.Printf("	return\n")
	g.Printf("}\n")
	g.Printf("class_%s = clazz\n", cls.JNIName)
	for _, f := range cls.Funcs {
		if !f.Public || !g.isFuncSupported(f) {
			continue
		}
		g.Printf("{\n")
		g.Indent()
		name := f.Name
		if f.Constructor {
			name = "<init>"
		}
		g.Printf("fn := C.CString(%q)\n", name)
		g.Printf("fd := C.CString(%q)\n", f.Desc)
		if f.Constructor {
			g.Printf("m := C.go_seq_get_method_id(clazz, fn, fd)\n")
		} else {
			g.Printf("m := C.go_seq_get_static_method_id(clazz, fn, fd)\n")
		}
		g.Printf("C.free(unsafe.Pointer(fn))\n")
		g.Printf("C.free(unsafe.Pointer(fd))\n")
		g.Printf("if m != nil {\n")
		g.Indent()
		g.Printf("%s.%s = func", cls.PkgName, f.GoName)
		g.genFuncDecl(false, f)
		g.Printf(" {\n")
		g.Indent()
		for i, a := range f.Params {
			g.genWrite(fmt.Sprintf("a%d", i), a, modeTransient)
		}
		g.Printf("res := C.cproxy_s_%s_%s(clazz, m", cls.JNIName, f.JNIName)
		for i := range f.Params {
			g.Printf(", _a%d", i)
		}
		g.Printf(")\n")
		g.genFuncRet(f)
		g.Outdent()
		g.Printf("}\n")
		g.Outdent()
		g.Printf("}\n")
		g.Outdent()
		g.Printf("}\n")
	}
	g.Printf("%s.Cast = func(v interface{}) Java.%s {\n", cls.PkgName, goClsName(cls.Name))
	g.Indent()
	g.Printf("t := reflect.TypeOf((*proxy_class_%s)(nil))\n", cls.JNIName)
	g.Printf("cv := reflect.ValueOf(v).Convert(t).Interface().(*proxy_class_%s)\n", cls.JNIName)
	g.Printf("ref := C.jint(_seq.ToRefNum(cv))\n")
	g.Printf("if C.go_seq_isinstanceof(ref, class_%s) != 1 {\n", cls.JNIName)
	g.Printf("	panic(fmt.Errorf(\"%%T is not an instance of %%s\", v, %q))\n", cls.Name)
	g.Printf("}\n")
	g.Printf("return cv\n")
	g.Outdent()
	g.Printf("}\n")
	g.Outdent()
	g.Printf("}\n\n")
	g.Printf("type proxy_class_%s _seq.Ref\n\n", cls.JNIName)
	g.Printf("func (p *proxy_class_%s) Bind_proxy_refnum__() int32 { return (*_seq.Ref)(p).Bind_IncNum() }\n\n", cls.JNIName)
	for _, f := range cls.AllMethods {
		if !g.isFuncSupported(f) {
			continue
		}
		g.Printf("func (p *proxy_class_%s) %s", cls.JNIName, f.GoName)
		g.genFuncDecl(false, f)
		g.genFuncBody(cls, f, "cproxy")
	}
	if cls.Throwable {
		g.Printf("func (p *proxy_class_%s) Error() string {\n", cls.JNIName)
		g.Printf("	return p.ToString()\n")
		g.Printf("}\n")
	}
	if cls.HasSuper() {
		g.Printf("func (p *proxy_class_%s) Super() Java.%s {\n", cls.JNIName, goClsName(cls.Name))
		g.Printf("	return &super_%s{p}\n", cls.JNIName)
		g.Printf("}\n\n")
		g.Printf("type super_%s struct {*proxy_class_%[1]s}\n\n", cls.JNIName)
		for _, f := range cls.AllMethods {
			if !g.isFuncSupported(f) {
				continue
			}
			g.Printf("func (p *super_%s) %s", cls.JNIName, f.GoName)
			g.genFuncDecl(false, f)
			g.genFuncBody(cls, f, "csuper")
		}
	}
}

func (g *ClassGen) genFuncBody(cls *java.Class, f *java.Func, prefix string) {
	g.Printf(" {\n")
	g.Indent()
	for i, a := range f.Params {
		g.genWrite(fmt.Sprintf("a%d", i), a, modeTransient)
	}
	g.Printf("res := C.%s_%s_%s(C.jint(p.Bind_proxy_refnum__())", prefix, cls.JNIName, f.JNIName)
	for i := range f.Params {
		g.Printf(", _a%d", i)
	}
	g.Printf(")\n")
	g.genFuncRet(f)
	g.Outdent()
	g.Printf("}\n\n")
}

func (g *ClassGen) genFuncRet(f *java.Func) {
	if f.Ret != nil {
		g.genRead("_res", "res.res", f.Ret, modeRetained)
		g.genRefRead("_exc", "res.exc", "error", "proxy_error", true)
	} else {
		g.genRefRead("_exc", "res", "error", "proxy_error", true)
	}
	if f.Throws == "" {
		g.Printf("if (_exc != nil) { panic(_exc) }\n")
		if f.Ret != nil {
			g.Printf("return _res\n")
		}
	} else {
		if f.Ret != nil {
			g.Printf("return _res, _exc\n")
		} else {
			g.Printf("return _exc\n")
		}
	}
}

func (g *ClassGen) genRead(to, from string, t *java.Type, mode varMode) {
	switch t.Kind {
	case java.Int, java.Short, java.Char, java.Byte, java.Long, java.Float, java.Double:
		g.Printf("%s := %s(%s)\n", to, g.goType(t, false), from)
	case java.Boolean:
		g.Printf("%s := %s != C.JNI_FALSE\n", to, from)
	case java.String:
		g.Printf("%s := decodeString(%s)\n", to, from)
	case java.Array:
		if t.Elem.Kind != java.Byte {
			panic("unsupported array type")
		}
		g.Printf("%s := toSlice(%s, %v)\n", to, from, mode == modeRetained)
	case java.Object:
		_, hasProxy := g.imported[t.Class]
		g.genRefRead(to, from, g.goType(t, false), "proxy_class_"+flattenName(t.Class), hasProxy)
	default:
		panic("invalid kind")
	}
}

func (g *ClassGen) genRefRead(to, from string, intfName, proxyName string, hasProxy bool) {
	g.Printf("var %s %s\n", to, intfName)
	g.Printf("%s_ref := _seq.FromRefNum(int32(%s))\n", to, from)
	g.Printf("if %s_ref != nil {\n", to)
	g.Printf("	if %s < 0 { // go object\n", from)
	g.Printf("		%s = %s_ref.Get().(%s)\n", to, to, intfName)
	g.Printf("	} else { // foreign object\n")
	if hasProxy {
		g.Printf("		%s = (*%s)(%s_ref)\n", to, proxyName, to)
	} else {
		g.Printf("		%s = %s_ref\n", to, to)
	}
	g.Printf("	}\n")
	g.Printf("}\n")
}

func (g *ClassGen) genWrite(v string, t *java.Type, mode varMode) {
	switch t.Kind {
	case java.Int, java.Short, java.Char, java.Byte, java.Long, java.Float, java.Double:
		g.Printf("_%s := C.%s(%s)\n", v, t.CType(), v)
	case java.Boolean:
		g.Printf("_%s := C.jboolean(C.JNI_FALSE)\n", v)
		g.Printf("if %s {\n", v)
		g.Printf("	_%s = C.jboolean(C.JNI_TRUE)\n", v)
		g.Printf("}\n")
	case java.String:
		g.Printf("_%s := encodeString(%s)\n", v, v)
	case java.Array:
		if t.Elem.Kind != java.Byte {
			panic("unsupported array type")
		}
		g.Printf("_%s := fromSlice(%s, %v)\n", v, v, mode == modeRetained)
	case java.Object:
		g.Printf("var _%s C.jint = _seq.NullRefNum\n", v)
		g.Printf("if %s != nil {\n", v)
		g.Printf("	_%s = C.jint(_seq.ToRefNum(%s))\n", v, v)
		g.Printf("}\n")
	default:
		panic("invalid kind")
	}
}

// genCRetClear clears the result value from a JNI call if an exception was
// raised.
func (g *ClassGen) genCRetClear(v string, t *java.Type, exc string) {
	g.Printf("if (%s != NULL) {\n", exc)
	g.Indent()
	switch t.Kind {
	case java.Int, java.Short, java.Char, java.Byte, java.Long, java.Float, java.Double, java.Boolean:
		g.Printf("%s = 0;\n", v)
	default:
		// Assume a nullable type. It will break if we missed a type.
		g.Printf("%s = NULL;\n", v)
	}
	g.Outdent()
	g.Printf("}\n")
}

func (g *ClassGen) genJavaToC(v string, t *java.Type) {
	switch t.Kind {
	case java.Int, java.Short, java.Char, java.Byte, java.Long, java.Float, java.Double, java.Boolean:
		g.Printf("%s _%s = %s;\n", t.JNIType(), v, v)
	case java.String:
		g.Printf("nstring _%s = go_seq_from_java_string(env, %s);\n", v, v)
	case java.Array:
		if t.Elem.Kind != java.Byte {
			panic("unsupported array type")
		}
		g.Printf("nbyteslice _%s = go_seq_from_java_bytearray(env, %s, 1);\n", v, v)
	case java.Object:
		g.Printf("jint _%s = go_seq_to_refnum(env, %s);\n", v, v)
	default:
		panic("invalid kind")
	}
}

func (g *ClassGen) genCToJava(v string, t *java.Type) {
	switch t.Kind {
	case java.Int, java.Short, java.Char, java.Byte, java.Long, java.Float, java.Double, java.Boolean:
		g.Printf("%s _%s = %s;\n", t.JNIType(), v, v)
	case java.String:
		g.Printf("jstring _%s = go_seq_to_java_string(env, %s);\n", v, v)
	case java.Array:
		if t.Elem.Kind != java.Byte {
			panic("unsupported array type")
		}
		g.Printf("jbyteArray _%s = go_seq_to_java_bytearray(env, %s, 0);\n", v, v)
	case java.Object:
		g.Printf("jobject _%s = go_seq_from_refnum(env, %s, NULL, NULL);\n", v, v)
	default:
		panic("invalid kind")
	}
}

func goClsName(n string) string {
	return initialUpper(strings.Replace(n, ".", "_", -1))
}

func (g *ClassGen) genInterface(cls *java.Class) {
	g.Printf("type %s interface {\n", goClsName(cls.Name))
	g.Indent()
	// Methods
	for _, f := range cls.AllMethods {
		if !g.isFuncSupported(f) {
			continue
		}
		g.Printf(f.GoName)
		g.genFuncDecl(true, f)
		g.Printf("\n")
	}
	if cls.HasSuper() {
		g.Printf("Super() %s\n", goClsName(cls.Name))
	}
	if cls.Throwable {
		g.Printf("Error() string\n")
	}
	g.Outdent()
	g.Printf("}\n\n")
}

// Flatten java class names. "java.package.Class$Inner" is converted to
// "java_package_Class_Inner"
func flattenName(n string) string {
	return strings.Replace(strings.Replace(n, ".", "_", -1), "$", "_", -1)
}

var (
	classesPkgHeader = `// File is generated by gobind. Do not edit.

package Java

// Used to silence this package not used errors
const Dummy = 0

`
	classesCHeader = `// File is generated by gobind. Do not edit.

#include <jni.h>
#include "seq.h"
#include "classes.h"

`
	classesHHeader = `// File is generated by gobind. Do not edit.

#include <jni.h>
#include "seq.h"

extern void init_proxies();

`

	javaImplHeader = `// File is generated by gobind. Do not edit.

`

	classesGoHeader = `// File is generated by gobind. Do not edit.

package gomobile_bind

/*
#include <stdlib.h> // for free()
#include <jni.h>
#include "seq.h"
#include "classes.h"
*/
import "C"

import (
	"Java"
	_seq "golang.org/x/mobile/bind/seq"
)

`
)
